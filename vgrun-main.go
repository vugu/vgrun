package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"

	"github.com/fsnotify/fsnotify"
)

var flagV = flag.Bool("v", false, "Verbose output")

func main() {

	// TODO: set flag.Usage
	// TODO: verbose flag
	flagInstallTools := flag.Bool("install-tools", false, "Installs common Vugu tools using `go install`")
	flagNoGenerate := flag.Bool("no-generate", false, "Disable `go generate`")
	flagBinDir := flag.String("bin-dir", "bin", "Directory of where to place built binary")
	flag1 := flag.Bool("1", false, "Run only once and exit after")
	flagAutoReloadAt := flag.String("auto-reload-at", "localhost:8324", "Run auto-reload server using this listener.  An empty string will disable it.")
	flagNewFromExample := flag.String("new-from-example", "", "Initialize a new project from example.  Will git clone from github.com/vugu-examples/[value] or if value contains a slash it will be treated as a full URL sent to git clone.  Must be followed by empty or non existent target directory.")
	flagWatchPattern := flag.String("watch-pattern", "\\.vugu$", "Sets the regexp pattern of files to watch")
	flagWatchDir := flag.String("watch-dir", ".", "Specifies which directory to watch from")
	flag.Parse()

	// build directory (and exe name) is first and only arg; or if it ends with .go then that file
	// is run with `go run`; for now no default behavior, no arg is an error

	// TODO: what about something like:
	// vgrun server -some-server-opt

	if *flagInstallTools {

		b, err := exec.Command("go", "install", "-x", "github.com/vugu/vugu/cmd/vugugen").CombinedOutput()
		if err != nil {
			log.Fatalf("Error installing vugugen: %v; full output:\n%s", err, b)
		}

		b, err = exec.Command("go", "install", "-x", "https://github.com/vugu/vgrouter/cmd/vgrgen").CombinedOutput()
		if err != nil {
			log.Fatalf("Error: %v; full output:\n%s", err, b)
		}

		return
	}

	if *flagNewFromExample != "" {
		panic(fmt.Errorf("not yet implemented"))
	}

	args := flag.Args()
	if len(args) < 1 {
		log.Fatalf("You must provide something to run, either the path to the main package or a .go file.")
	}

	ru := newRunner()
	ru.binDir = *flagBinDir
	ru.generateDir = "."
	if *flagNoGenerate {
		ru.generateDir = ""
	}
	ru.buildTarget = args[0]
	ru.args = args[1:]

	// // only one run requested
	// if *flag1 {
	// 	err := ru.generateAndBuild()
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}
	// 	err = ru.runOnce()
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}
	// 	return
	// }

	ar := newAutoReloader()
	ru.setPider = ar

	// only watch if not -1
	if !*flag1 {
		watchPattern := regexp.MustCompile(*flagWatchPattern)

		if *flagWatchDir == "" {
			log.Fatal("You must specify a watch dir in order to watch")
		}
		rwatcher, err := NewRWatcher()
		if err != nil {
			log.Fatal(err)
		}
		rwatcher.AddRecursive(*flagWatchDir)

		go func() {
		watchLoop:
			for {
				select {

				case event := <-rwatcher.Events:

					if *flagV {
						log.Printf("DEBUG: watcher: %q %v", event.Name, event.Op)
					}

					switch event.Op {
					case fsnotify.Create:
					case fsnotify.Remove:
					case fsnotify.Write:
					default:
						continue // ignore others
					}

					if watchPattern.MatchString(event.Name) {

						if *flagV {
							log.Printf("watcher: %q %v, rebuilding and restarting...", event.Name, event.Op)
						} else {
							log.Printf("Generate and Rebuild: %s", event.Name)
						}

						// consume anything on the state change channel
						for len(ru.runStateUpdateCh) > 0 {
							rs := <-ru.runStateUpdateCh
							_ = rs
							// log.Printf("runState changed to: %v", rs)
						}

						// ask the runner to rebuild and restart
						ru.runStateChangeReqCh <- runStateChangeReqRebuildAndRestart

						// read its events until it tells us that it restarted
					waitRunStateChanges:
						for rs := range ru.runStateUpdateCh {
							// log.Printf("runState changed to: %v", rs)
							switch rs {

							// if rebuild fail we just go back to waiting for events
							case runStateRebuildFail:
								continue watchLoop

							case runStateNone, runStateRebuildSuccess:
								// doesn't mean anything for us
								continue

							case runStateRunning:
								// it's back up, drop through and send message to any browsers listening
								break waitRunStateChanges

							}
						}

						// err := ru.generateAndBuild()
						// if err != nil {
						// 	log.Printf("generate/build error: %v", err)
						// 	break
						// }

						// err = ru.restart()
						// if err != nil {
						// 	log.Printf("restart error: %v", err)
						// 	break
						// }

						// let's give the process just a real brief moment to fire up the webserver
						// time.Sleep(time.Millisecond * 200)

						// tell the browser to refresh
						// FIXME: this should be protected by a mutex
						// if ru.cmd != nil && ru.cmd.Process != nil {
						// 	// ar.push([]byte(fmt.Sprintf(`{"type":"exec","pid":%v}`, ru.cmd.Process.Pid)))
						// 	ar.setPid(ru.cmd.Process.Pid)
						// }

						// drain the channel to len 0 before continuing - we don't want a bunch of
						// file change events stacked up while we were waiting for the build
						for len(rwatcher.Events) > 0 {
							<-rwatcher.Events
						}

					}

				case err := <-rwatcher.Errors:
					log.Printf("watcher error: %v", err)

				}
			}
		}()

	}

	if *flagV {
		log.Printf("Starting auto-reload server at %q", *flagAutoReloadAt) // should be only in verbose mode
	}
	go func() {
		log.Fatal(http.ListenAndServe(*flagAutoReloadAt, ar))
	}()

	err := ru.run()
	if err != nil {
		log.Fatal(err)
	}

	// var wa watcher
	// wa.watchDir = *flagWatchDir
	// wa.watchPattern = regexp.MustCompile(*flagWatchPattern)

	// do generate and build

	// start file watcher if requested

	// run the executable

	// do the generate

	// // see if this is a go file, e.g. vgrun devserver.go
	// if path.Ext(args[0]) == ".go" {
	// 	// go run
	// }

	// go build

	// then execute file

}

/*

NOTES:

- figure out which command line library (kingpin, viper, cobra, etc.)
  maybe: https://github.com/alecthomas/kong  - interesting but might be overkill,
  if there is no subcommand (because we want `vgrun` with no args or with one simple option
  to have expected behavior, then maybe -init becomes and option and we don't need subcommands,
  just stick with the flag package) STICK WITH FLAG PKG

- run/build command does:
  starts file system watcher USE FSNOTIFY
  does generate (maybe need to hit a couple different folders), build and run in a loop
    (should check for various defaults like devserver.go, or the presence of the server and client dirs etc)
    ONLY ONE GENERATE FOR NOW, BY DEFAULT IT'S THE FOLDER YOU'RE IN AND PEOPLE SHOULD JUST PUT
    ALL THIER STUFF IN generate.go (argument: vugu project sub-packages often depend on each other
    and it's rarely needed to generate them separately, if that comes up later you can always move
    some infrequently-changing stuff out and run go generate it by hand)
  -l option will fire up the listener and serve refresh thingy - actually this should be default behavior with
  switch to disable it

- maybe we have to go with this: https://medium.com/@skdomino/watch-this-file-watching-in-go-5b5a247cf71f
  and do the recursive directory watch stuff (and if a dir changes just re-register everything)
  https://github.com/fsnotify/fsnotify

- while watching for files, do we automatically call go generate every time any file changes? or only .vugu
  files?  probably an option in here somewhere plus some sensible defaults
  RUN ONCE AT STARTUP AND THEN ONLY WHEN VUGU FILES CHANGE, OPTION TO OVERRIDE FILE MATCH PATTERN

- should we be calling go generate? or do we leave that to the WasmCompiler? it's two different things
  (client an server) and they may or may not overlap; do we have to worry about two go generates happening
  at the same time? (options to solve include: vgrun could kill the process and wait or
  do some strange locking)
  BOTH SHOULD CALL THE EXACT SAME `go generate` CMD IN THE PROJECT ROOT.
  FOR THE MOMENT IGNORE THE RACE CONDITION AND LET'S SEE IF IT'S AN ISSUE

- we should probably put executable(s) in a bin folder by convention, just so they aren't laying around
  YES, ./bin

- webserver:
  * static script that opens a websocket and when a message comes refreshes the page
  * websocket to which we push a message when we notice a file change

- should try to automatically install vugugen vgrgen and anything else, or at least have an option for this,
  (maybe when using 'init')
  DO IT WITH INIT, PROVIDE A -install-tools OPTION AND OTHERWISE CHECK FOR AND HINT IF WE CAN'T FIND IT
  UPON NORMAL RUN ("Warning: vugugen not detected, you can install it and other tools with vgrun -install-tools")

- what if we had an '-init' or 'create' or something and you put in the name of the example and it
  just copied it down for you - super simple, just error if the dir isn't empty, etc.
  (we only need one simple example to start and make this work, then we can build out the rest of the
  examples - probably we need github.com/vugu-examples/[example] name and then we git clone it out
  and remove the .git folder - bam, super simple)
  WE DEFINITELY ARE DOING THIS

*/

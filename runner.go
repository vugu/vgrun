package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

/*
- need to distinguish between the process exiting itself and us killing it.
- if the process exits itself we should not run it again we should exit
*/

type runner struct {
	generateDir string   // run go generate in this folder, empty means disable, "." means cur dir
	binDir      string   // where to write output files
	buildTarget string   // either "filename.go" or e.g. "server" which is dir name of main pkg
	args        []string // cmdline args to be passed when running
	// rwmu        sync.RWMutex
	// looping     bool      // false when stop() is called
	cmd *exec.Cmd // actively running command

	// pid              int                 // pid of the currently running process or 0 if not running
	runState            runState               // current state
	runStateUpdateCh    chan runState          // state changes are sent here
	runStateChangeReqCh chan runStateChangeReq // request state changes with this

	setPider setPider
}

type setPider interface {
	setPid(pid int)
}

type runState int

const (
	runStateNone           = runState(iota) // not running
	runStateRunning                         // process is running
	runStateRebuildSuccess                  // rebuild worked successfully, will only be in this state briefly then back to Running
	runStateRebuildFail                     // generate or build failed (but prior process still running)
	// runStateRebuilding                  // rebuild in progress
	// runStateStopping                    // process is being stopped
)

// run state change request
type runStateChangeReq int

const (
	runStateChangeReqStop = runStateChangeReq(iota)
	runStateChangeReqRebuildAndRestart
)

func newRunner() *runner {
	return &runner{
		runStateUpdateCh:    make(chan runState, 32),
		runStateChangeReqCh: make(chan runStateChangeReq, 1),
	}
}

// setState updates runner state and notifies any observers via runStateUpdateCh
// It should never block.  Log a fatal error if the channel ever fills up.
func (ru *runner) setState(newState runState) {
	if ru.runState == newState {
		log.Printf("Runner prev state == requested new state.  State = %v", newState)
	}
	if len(ru.runStateUpdateCh) == cap(ru.runStateUpdateCh) {
		log.Fatalf("State updates channel already full (%d messages), consumer process blocked, exiting",
			cap(ru.runStateUpdateCh))
	}
	ru.runState = newState
	select { // non-blocking send
	case ru.runStateUpdateCh <- ru.runState:
	default:
	}
}

// func (ru *runner) isGoRunTarget() bool {
// 	return filepath.Ext(ru.buildTarget) == ".go"
// }

// run is the main run loop
func (ru *runner) run() error {

	// state must be runStateNone
	if ru.runState != runStateNone {
		return fmt.Errorf("unexpected start state: %v", ru.runState)
	}

	defer func() {
		ru.setState(runStateNone)
	}()

	// keeps track of which command is currently running (if any)
	var cmd *exec.Cmd
	// the error returned from Wait() when th process exits
	var cmdErrCh chan error

	for {

		err := ru.generateAndBuild()
		if err != nil {
			// on error if process not running, exit
			if cmd == nil {
				return fmt.Errorf("initial build error: %w", err)
			}
			log.Printf("generate or build failure:\n%v", err)
			// if process still running but generateAndBuild failed, we skip over the process start
			// and just wait for events again

			ru.setState(runStateRebuildFail)

			goto waitForIt
		}

		ru.setState(runStateRebuildSuccess)

		// build was successful, we now need to stop the prior running process if applicable
		if cmd != nil {
			//if *flagV {
			//	log.Printf("about to perform gracefulStop on pid=%v", cmd.Process.Pid)
			//}
			gracefulStop(cmd.Process, cmdErrCh, time.Second*10)
		}

		{
			// new command, new channel
			cmd = nil
			cmdErrCh = make(chan error, 1)

			// attempt to run process
			// if ru.isGoRunTarget() {
			// 	args := []string{"run", ru.buildTarget}
			// 	args = append(args, ru.args...)
			// 	cmd = exec.Command("go", args...)
			// } else {
			cmd = exec.Command(filepath.Join(ru.binDir, strings.TrimSuffix(filepath.Base(ru.buildTarget), ".go")+exeSuffix()), ru.args...)
			// }
			ru.cmd = cmd

			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			err = cmd.Start()
			if err != nil {
				// process start error is always an immediate exit
				return fmt.Errorf("process start error: %w", err)
			}

			// whenever we have a new pid, we tell the auto-reloader about it
			ru.setPider.setPid(cmd.Process.Pid)

			// wait in goroutine (convert blocking call to channel so we can `select` below)
			go func() {
				err := cmd.Wait()
				select { // non-blocking send
				case cmdErrCh <- err:
				default:
				}

			}()
		}

		time.Sleep(1 * time.Second)  // let client start refresh
		ru.setState(runStateRunning) // before host starts listening for source changes again

	waitForIt:
		select {

		// we've been asked to change the state while running
		case req := <-ru.runStateChangeReqCh:

			switch req {

			// if they asked us to stop we're done
			case runStateChangeReqStop:
				gracefulStop(cmd.Process, cmdErrCh, time.Second*10)
				return nil

			// they asked us to rebuild+restart
			case runStateChangeReqRebuildAndRestart:
				// fall through to top of loop

			default:
				panic(fmt.Errorf("unknown state change request: %v", req))

			}

		// process exited on it's own
		case err := <-cmdErrCh:
			// we always just exit in this case
			// if *flagV {
			// 	log.Printf("Process exited by itself: %v", err)
			// }
			if err != nil {
				return fmt.Errorf("unexpected process exit: %w", err)
			}
			return err
		}

	}

	// unreachable

}

func (ru *runner) generateAndBuild() (reterr error) {

	if *flagV {
		log.Printf("Running generateAndBuild")
		defer func() {
			log.Printf("Exiting generateAndBuild (err=%v)", reterr)
		}()
	}

	if ru.generateDir != "" {
		cmd := exec.Command("go", "generate")
		cmd.Dir = ru.generateDir
		if *flagV {
			log.Printf("About to execute go: %v", cmd.Args)
		}
		b, err := cmd.CombinedOutput()
		if err != nil {
			if *flagV {
				log.Printf("generateAndBuild error: %v", err)
			}
			return fmt.Errorf("generate error: %w; full output:\n%s", err, b)
		}
	}

	if ru.buildTarget == "" {
		return fmt.Errorf("empty buildTarget")
	}

	// // for .go files there is no build step
	// if ru.isGoRunTarget() {
	// 	return nil
	// }

	absBinDir, err := filepath.Abs(ru.binDir)
	if err != nil {
		if *flagV {
			log.Printf("generateAndBuild filepath.Abs(ru.binDir) error: %v", err)
		}
		return err
	}

	// create bin dir if it doesn't exist, but do not try to create parent dirs
	os.Mkdir(absBinDir, 0755)

	outBase := strings.TrimSuffix(filepath.Base(ru.buildTarget), ".go")
	var cmd *exec.Cmd
	if filepath.Ext(ru.buildTarget) == ".go" {
		cmd = exec.Command("go", "build", "-o", filepath.Join(absBinDir, outBase)+exeSuffix(), ru.buildTarget) // .go file
	} else {
		cmd = exec.Command("go", "build", "-o", filepath.Join(absBinDir, outBase)+exeSuffix()) // package
		cmd.Dir, err = filepath.Abs(ru.buildTarget)
		if err != nil {
			return fmt.Errorf("unable to translate %q to an absolute path: %w", ru.buildTarget, err)
		}
	}
	if *flagV {
		log.Printf("About to execute go: %v (dir=%v)", cmd.Args, cmd.Dir)
	}
	b, err := cmd.CombinedOutput()
	if err != nil {
		if *flagV {
			log.Printf("generateAndBuild go build error: %v", err)
		}
		return fmt.Errorf("build error: %w; full output:\n%s", err, b)
	}

	return nil
}

// gracefulStop tries to stop a process using SIGINT and if that fails
// SIGKILL, blocks until ch returns something (process dead)
func gracefulStop(proc *os.Process, ch chan error, timeout time.Duration) {

	var err error

	if *flagV {
		log.Printf("gracefulStop running on pid=%v", proc.Pid)
	}

	if runtime.GOOS != "windows" {
		err = proc.Signal(os.Interrupt)
		if err != nil {
			log.Printf("Signal error: %v", err)
			goto kill
		}
		if *flagV {
			log.Printf("gracefulStop Signal(os.Interrupt) ok, waiting for error from channel")
		}

		select {
		case err = <-ch:
			goto reportErr
		case <-time.After(timeout):
			log.Printf("gracefulStop hit timeout")
			goto kill
		}
	} else { // os.Interrupt not supported for Windows?
		goto kill
	}

kill:
	log.Printf("gracefulStop doing kill")
	err = proc.Signal(os.Kill)
	if err != nil {
		// FIXME: ideally we would make sure this is not an error saying the process already exited
		if runtime.GOOS == "windows" && err.Error() == "invalid argument" {
			if *flagV {
				log.Printf("ignoring windows error %v during kill", err)
			}
		} else {
			panic(err)
		}
	}

	if *flagV {
		log.Printf("gracefulStop waiting for error from channel")
	}

	err = <-ch
reportErr:
	if *flagV {
		if err != nil {
			log.Printf("Process exited with error: %v", err)
		} else {
			log.Printf("Process exited cleanly")
		}
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// func (ru *runner) runLoop() error {

// 	ru.rwmu.Lock()
// 	ru.looping = true
// 	ru.rwmu.Unlock()

// 	defer func() {
// 		ru.rwmu.Lock()
// 		ru.looping = false
// 		ru.rwmu.Unlock()
// 	}()

// 	looping := true
// 	for looping { // TODO: how do we know when to exit

// 		ru.rwmu.RLock()
// 		looping = ru.looping
// 		ru.rwmu.RUnlock()

// 		log.Printf("Running process...")
// 		err := ru.runOnce()
// 		if err != nil {
// 			log.Printf("Process run error: %v", err)
// 			time.Sleep(5 * time.Second)
// 			continue
// 		}
// 	}

// 	return nil
// }

// // runs and blocks until execution finishes
// func (ru *runner) runOnce() error {

// 	var cmd *exec.Cmd

// 	if ru.isGoRunTarget() {
// 		args := []string{"run", ru.buildTarget}
// 		args = append(args, ru.args...)
// 		cmd = exec.Command("go", args...)
// 	} else {
// 		cmd = exec.Command(filepath.Join(ru.binDir, filepath.Base(ru.buildTarget)), ru.args...)
// 	}

// 	cmd.Stdin = os.Stdin
// 	cmd.Stdout = os.Stdout
// 	cmd.Stderr = os.Stderr

// 	err := cmd.Start()
// 	if err != nil {
// 		return fmt.Errorf("process start error: %w", err)
// 	}

// 	ru.rwmu.Lock()
// 	ru.cmd = cmd
// 	ru.rwmu.Unlock()

// 	return cmd.Wait()

// }

// func (ru *runner) stop() error {

// 	ru.rwmu.Lock()
// 	cmd := ru.cmd
// 	ru.looping = false
// 	ru.rwmu.Unlock()

// 	if cmd == nil {
// 		return nil
// 	}

// 	defer func() {
// 		ru.rwmu.Lock()
// 		ru.cmd = nil
// 		ru.rwmu.Unlock()
// 	}()

// 	return cmd.Process.Kill()
// }

// // restart will stop the running process and start it again.
// // it blocks until the process is started again or error.
// // will not restart unless looping is true (there is an active runLoop running)
// func (ru *runner) restart() error {

// 	ru.rwmu.RLock()
// 	cmd := ru.cmd
// 	ru.rwmu.RUnlock()

// 	if cmd == nil {
// 		return nil
// 	}

// 	err := cmd.Process.Kill()
// 	if err != nil {
// 		return err
// 	}

// 	ru.rwmu.Lock()
// 	looping := ru.looping
// 	ru.rwmu.Unlock()

// 	if !looping {
// 		return nil
// 	}

// 	for i := 0; i < 10; i++ {

// 		ru.rwmu.RLock()
// 		newcmd := ru.cmd
// 		ru.rwmu.RUnlock()

// 		// check to see if we see new process running
// 		if newcmd != nil && newcmd != cmd {
// 			break
// 		}

// 		time.Sleep(time.Second)
// 	}

// 	return nil
// }

// two steps:
// 1. generate+build
// 2. run (could be exe or `go run devserver.go`)

// func (ru *runner) Run() error {
// 	panic(fmt.Errorf("not yet implemented"))
// }

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRWatcher(t *testing.T) {

	// FIXME: definitely need to clean this test up and try a bunch of cases

	tmpDir, err := ioutil.TempDir("", "TestRWatcher")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	t.Logf("Using tmpDir: %s", tmpDir)

	rw, err := NewRWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()

	cancel := make(chan struct{}, 1)

	go func() {
	egress:
		for {
			select {

			case <-cancel:
				break egress

			case event := <-rw.Events:

				fmt.Printf("EVENT! %#v\n", event)

			case err := <-rw.Errors:
				fmt.Println("ERROR", err)

			}
		}
	}()

	err = rw.AddRecursive(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	os.Mkdir(filepath.Join(tmpDir, "a"), 0755)

	time.Sleep(time.Millisecond * 50)

	err = ioutil.WriteFile(filepath.Join(tmpDir, "a/example.txt"), []byte("test"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second * 5)
	cancel <- struct{}{}

}

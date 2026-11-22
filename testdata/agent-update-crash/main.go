package main

import (
	"encoding/json"
	"os"
	"runtime"
	"time"
)

var mode = "crash"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "build-info" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": "0.1.0", "revision": "dev", "os": "linux", "arch": runtime.GOARCH, "updateProtocol": 1})
		return
	}
	if mode == "hang" {
		time.Sleep(10 * time.Minute)
	}
	os.Exit(42)
}

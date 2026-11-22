package main

import (
	"encoding/json"
	"os"
)

func main() {
	b, err := json.Marshal(os.Args[1:])
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("TOKEN_MONITOR_TEST_REGISTRATION"), b, 0600); err != nil {
		panic(err)
	}
}

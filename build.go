package main

import (
	"errors"
	"log"
	"os/exec"
)

func vulcanize() ([]byte, error) {
	log.Println("Vulcanizing...")
	cmd := exec.Command("node_modules/vulcanize/bin/vulcanize", "--inline-scripts", "--inline-css", "public/app.html")
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		log.Println("STDOUT ", string(stdout))
		return nil, errors.New(err.Error() + "\n" + string(stdout))
	}
	return stdout, nil
}

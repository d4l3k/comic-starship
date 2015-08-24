package main

import (
	"log"
	"os/exec"
)

func vulcanize() ([]byte, error) {
	log.Println("Vulcanizing...")
	cmd := exec.Command("node_modules/vulcanize/bin/vulcanize", "--inline-scripts", "--inline-css", "public/app.html")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return []byte(stdout), nil
}

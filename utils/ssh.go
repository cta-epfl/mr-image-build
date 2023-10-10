package utils

import (
	"log"
	"os"
)

func InitSshConfig() {
	err := os.Mkdir("/home/app/.ssh", 0700)
	if err != nil {
		log.Printf("Error creating .ssh folder: %s\n", err)
	}

	fileConfig, _ := os.Create("/home/app/.ssh/config")
	fileConfig.Write([]byte("IdentityFile /home/app/.ssh/id_ecdsa\n"))

	fileKnownHosts, _ := os.Create("/home/app/.ssh/known_hosts")
	fileKnownHosts.Write([]byte(os.Getenv("FLUX_KNOWN_HOSTS")))

	fileEcdsa, _ := os.Create("/home/app/.ssh/id_ecdsa")
	fileEcdsa.Write([]byte(os.Getenv("FLUX_IDENTITY")))
	fileEcdsa.Chmod(0600)

	fileEcdsaPub, _ := os.Create("/home/app/.ssh/id_ecdsa-pub")
	fileEcdsaPub.Write([]byte(os.Getenv("FLUX_IDENTITY_PUB")))
	fileEcdsaPub.Chmod(0644)

	// cmd := exec.Command("cp", "/app/ssh-keys/*", "/home/app/.ssh")
	// err := cmd.Run()
	// if err != nil{
	// 	log.Printf("Error while ssh config folder: %s", err)
	// }

	// fileConfig, _ := os.Create("/home/app/.ssh/config")
	// fileConfig.Write([]byte("IdentityFile /home/app/.ssh/identity\n"))

	// os.Chmod("/home/app/.ssh/identity", 0600)
}

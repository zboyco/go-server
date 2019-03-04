package main

import (
	"fmt"

	"github.com/zboyco/go-server/server"
)

func main() {
	fmt.Println("hello golang!!!")

	mainServer := server.New("127.0.0.1", 9043)

	mainServer.Start()
}

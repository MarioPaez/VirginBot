package main

import "github.com/MarioPaez/VirginBot/session"

func main() {
	ctx := session.DoLogin()
	session.FindClasses(ctx)
}

package main

type User struct {
	ID       int
	Username string
	Email    string
	Role     string
}

func newUser(username, email string) User {
	return User{Username: username, Email: email, Role: "member"}
}

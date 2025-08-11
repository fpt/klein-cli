package main

import (
	"fmt"
)

// User represents a user in the system
type User struct {
	ID    int
	Name  string
	Email string
	Age   int
}

// UserService handles user operations
type UserService struct {
	users []User
}

// NewUserService creates a new UserService
func NewUserService() *UserService {
	return &UserService{
		users: make([]User, 0),
	}
}

// AddUser adds a new user to the service
func (s *UserService) AddUser(id int, name, email string, age int) error {
	if id <= 0 {
		return fmt.Errorf("invalid user id: %d", id)
	}

	user := User{
		ID:    id,
		Name:  name,
		Email: email,
		Age:   age,
	}

	s.users = append(s.users, user)
	return nil
}

// GetUser retrieves a user by ID
func (s *UserService) GetUser(id int) (*User, error) {
	for i := range s.users {
		if s.users[i].ID == id {
			return &s.users[i], nil
		}
	}
	return nil, fmt.Errorf("user not found: %d", id)
}

// UpdateUserAge updates a user's age
func (s *UserService) UpdateUserAge(id, newAge int) error {
	user, err := s.GetUser(id)
	if err != nil {
		return err
	}

	user.Age = newAge
	return nil
}

// PrintUser prints user information
func PrintUser(user *User) {
	fmt.Printf("User ID: %d, Name: %s, Email: %s, Age: %d\n",
		user.ID, user.Name, user.Email, user.Age)
}

func main() {
	service := NewUserService()

	// Add some test users
	service.AddUser(1, "Alice Johnson", "alice@example.com", 25)
	service.AddUser(2, "Bob Smith", "bob@example.com", 30)

	// Get and print users
	user1, _ := service.GetUser(1)
	PrintUser(user1)

	user2, _ := service.GetUser(2)
	PrintUser(user2)

	// Update user age
	service.UpdateUserAge(1, 26)

	// Print updated user
	user1Updated, _ := service.GetUser(1)
	PrintUser(user1Updated)
}

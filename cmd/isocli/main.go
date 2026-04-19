package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("isocli — isomedia CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  isocli add-user <username>    Add a user")
	fmt.Println("  isocli list-users             List all users")
	fmt.Println("  isocli add-key <username>     Add an SSH key to a user")
	fmt.Println("  isocli remove-key <username>  Remove an SSH key from a user")
	fmt.Println()
	fmt.Println("Not yet implemented. Coming in a future phase.")
	os.Exit(0)
}

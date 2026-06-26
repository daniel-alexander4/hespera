package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("hescli — hespera CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  hescli add-user <username>    Add a user")
	fmt.Println("  hescli list-users             List all users")
	fmt.Println("  hescli add-key <username>     Add an SSH key to a user")
	fmt.Println("  hescli remove-key <username>  Remove an SSH key from a user")
	fmt.Println()
	fmt.Println("Not yet implemented. Coming in a future phase.")
	os.Exit(0)
}

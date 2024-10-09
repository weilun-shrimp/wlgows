package main

import "fmt"

func main() {
	// conn, err := client.Dial("ws", "localhost:8001", nil)
	// if err != nil {
	// 	fmt.Printf("%+v\n", err)
	// 	return
	// }
	// defer conn.Close()

	// for {
	// 	// Create a new reader that reads from standard input
	// 	reader := bufio.NewReader(os.Stdin)

	// 	fmt.Print("Enter your input: ")

	// 	// Read the input until a newline
	// 	input, err := reader.ReadString('\n')
	// 	if err != nil {
	// 		fmt.Println("Error reading input:", err)
	// 		return
	// 	}
	// 	fmt.Println("You entered:", input)
	// }

	// Original slice
	s := []int{1, 2, 3, 4, 5}

	// Slicing the original slice
	newSlice1 := s[1:4] // This will contain elements at index 1, 2, and 3
	newSlice2 := s[:3]  // This will contain elements at index 0, 1, and 2
	newSlice3 := s[2:]  // This will contain elements at index 2, 3, and 4

	fmt.Println("Original Slice:", s)
	fmt.Println("Sliced Slice 1:", newSlice1) // Output: [2 3 4]
	fmt.Println("Sliced Slice 2:", newSlice2) // Output: [1 2 3]
	fmt.Println("Sliced Slice 3:", newSlice3) // Output: [3 4 5]
}

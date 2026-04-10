package main

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
)

func main() {
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	res, _ := client.Do(context.Background(), "FT.SEARCH", "jobs", "*=>[KNN 1 @embedding $vec AS distance]", "PARAMS", "2", "vec", make([]byte, 768*4), "DIALECT", "2").Result()
	fmt.Printf("Type of res: %T\n", res)
	
	if m, ok := res.(map[interface{}]interface{}); ok {
	    fmt.Printf("It is a Map!\n")
	    _ = m
	}
	if s, ok := res.([]interface{}); ok {
	    fmt.Printf("It is a Slice! Length: %d, First Item Type: %T\n", len(s), s[0])
	}
}

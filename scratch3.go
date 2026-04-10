package main

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
)

func main() {
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	res, _ := client.Do(context.Background(), "FT.SEARCH", "jobs", "*=>[KNN 1 @embedding $vec AS distance]", "PARAMS", "2", "vec", make([]byte, 768*4), "RETURN", "5", "id", "source", "header", "content", "distance", "DIALECT", "2").Result()
	
	if m, ok := res.(map[interface{}]interface{}); ok {
	    for k, v := range m {
	        fmt.Printf("Key: %v (%T)\n", k, k)
			if kStr, ok := k.(string); ok && kStr == "results" {
				if s, ok := v.([]interface{}); ok {
					fmt.Printf("Results length: %d\n", len(s))
					if len(s) > 0 {
						if rm, ok := s[0].(map[interface{}]interface{}); ok {
							for rk, rv := range rm {
								fmt.Printf("  Result Key: %v (%T) -> %v (%T)\n", rk, rk, rv, rv)
							}
						}
					}
				}
			}
	    }
	}
}

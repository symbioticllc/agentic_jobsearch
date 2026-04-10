package main

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/leee/agentic-jobs/internal/store"
)

func main() {
	db, _ := store.NewSQLiteStore("./data/jobs.db")
	jobs, err := db.SearchFTS("Engineer")
	fmt.Printf("FTS Jobs: %d, Err: %v\n", len(jobs), err)

	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	res, err := client.Do(context.Background(), "FT.SEARCH", "jobs", "*=>[KNN 5 @embedding $vec AS distance]", "PARAMS", "2", "vec", make([]byte, 768*4), "RETURN", "5", "id", "source", "header", "content", "distance", "SORTBY", "distance", "ASC", "DIALECT", "2").Result()
	fmt.Printf("Redis Res Type: %T\nRedis Res: %+v\nError: %v\n", res, res, err)
}

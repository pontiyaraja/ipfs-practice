package main

import (
	"time"

	"github.com/go-redis/redis"
)

func Connect() *redis.Client {
	redisclient := redis.NewClient(&redis.Options{
		Addr:            "localhost:6379",
		Password:        "",
		DB:              0,
		MaxRetries:      5,
		MaxRetryBackoff: time.Duration(5 * time.Second),
		PoolTimeout:     time.Duration(3 * time.Second),
	})
	redisclient.Ping().Name()
	return redisclient
}

func lPop(listName string) (string, error) {
	c := connect()
	defer c.Close()
	strCmd := c.LPop(listName)
	return strCmd.Result()
	//fmt.Println(res, err)
}

func rPush(listName, value string) (int64, error) {
	c := connect()
	defer c.Close()
	strCmd := c.RPush(listName, value)
	return strCmd.Result()
	//fmt.Println(res, err)
}

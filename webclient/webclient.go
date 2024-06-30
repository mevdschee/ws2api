package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"sync"
)

func main() {
	n := 500
	var wg sync.WaitGroup
	wg.Add(n)
	client := &http.Client{}
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			response, err := client.Post("http://localhost:8080/localhost:8000/post_slow.php", "application/json", bytes.NewBuffer([]byte("{\"json\":true}")))
			if err != nil {
				log.Print(err.Error())
				return
			}
			bodyBytes, err := io.ReadAll(response.Body)
			if err != nil {
				log.Print(err.Error())
				return
			}
			log.Print(string(bodyBytes))
		}()
	}
	wg.Wait()
}

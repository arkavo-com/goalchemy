package main

import (
	"encoding/json" // want GCS002
	"errors"        // want GCS002
	"fmt"           // want GCS002
	"net/url"       // want GCS002
	"strings"       // want GCS002
	"time"          // want GCS002
	"unsafe"        // want GCS002

	"github.com/eugenioenko/goalchemy/lib/sync"
)

//go:noinline
func f() {} // want:-1 GCS001

func main() {
	var mu sync.Mutex
	fmt.Println(strings.ToUpper("x"), unsafe.Sizeof(0), errors.New("x"), time.Now(), json.Valid(nil), url.PathEscape("x"))
	_ = mu.TryLock // want GCS008
}

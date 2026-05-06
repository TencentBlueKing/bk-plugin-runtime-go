package pluginapi

import (
	"sync"

	"github.com/gin-gonic/gin"
)

type GinRegistrar func(gin.IRouter)

var (
	mu         sync.RWMutex
	registrars []GinRegistrar
)

func RegisterGin(registrar GinRegistrar) {
	mu.Lock()
	defer mu.Unlock()
	registrars = append(registrars, registrar)
}

func Registrars() []GinRegistrar {
	mu.RLock()
	defer mu.RUnlock()
	copied := make([]GinRegistrar, len(registrars))
	copy(copied, registrars)
	return copied
}

func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	registrars = nil
}

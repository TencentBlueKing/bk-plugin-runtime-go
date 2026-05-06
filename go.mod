module github.com/TencentBlueKing/bk-plugin-runtime-go

go 1.22

require (
	github.com/TencentBlueKing/bk-plugin-framework-go v0.5.0
	github.com/TencentBlueKing/blueapps-go v1.6.2
	github.com/gin-gonic/gin v1.10.0
	github.com/google/uuid v1.6.0
	github.com/pkg/errors v0.9.1
	github.com/samber/lo v1.38.1
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/cobra v1.8.1
	github.com/stretchr/testify v1.10.0
	gorm.io/driver/sqlite v1.5.7
	gorm.io/gorm v1.25.12
)

replace github.com/TencentBlueKing/bk-plugin-framework-go => ../bk-plugin-framework-go

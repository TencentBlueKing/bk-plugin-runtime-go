package main

import (
	_ "embed"
	"time"

	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-framework-go/kit"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/runner"
)

//go:embed inputs_form.json
var inputsForm []byte

type DemoPlugin struct{}

type ContextInputs struct {
	BizID int `json:"bk_biz_id"`
}

type Outputs struct {
	Message string `json:"message"`
}

func (p DemoPlugin) Version() string { return "1.0.0" }
func (p DemoPlugin) Desc() string    { return "legacy compatible plugin" }
func (p DemoPlugin) Execute(ctx *kit.Context) error {
	if ctx.InvokeCount() == 1 {
		ctx.WaitPoll(time.Second)
		return nil
	}
	return ctx.WriteOutputs(Outputs{Message: "done"})
}

func init() {
	hub.MustInstall(DemoPlugin{}, ContextInputs{}, Outputs{}, inputsForm)
}

func main() {
	runner.Run()
}

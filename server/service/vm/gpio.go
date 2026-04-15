package vm

import (
	"fmt"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/tinkerbell-community/NanoKVM/server/proto"
	"github.com/tinkerbell-community/NanoKVM/server/service/power"
)

func (s *Service) SetGpio(c *gin.Context) {
	var req proto.SetGpioReq
	var rsp proto.Response

	if err := proto.ParseFormRequest(c, &req); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("invalid arguments: %s", err))
		return
	}

	ctrl := power.GetController()
	var err error

	switch req.Action {
	case "on":
		err = ctrl.PowerOn()
	case "off", "forceoff":
		err = ctrl.PowerOff()
	case "reset":
		err = ctrl.Reset()
	default:
		rsp.ErrRsp(c, -2, fmt.Sprintf("invalid action: %s", req.Action))
		return
	}

	if err != nil {
		rsp.ErrRsp(c, -3, fmt.Sprintf("operation failed: %s", err))
		return
	}

	log.Debugf("power action %s completed", req.Action)
	rsp.OkRsp(c)
}

func (s *Service) GetGpio(c *gin.Context) {
	var rsp proto.Response

	ctrl := power.GetController()
	pwr, err := ctrl.State()
	if err != nil {
		rsp.ErrRsp(c, -2, fmt.Sprintf("failed to read power state: %s", err))
		return
	}

	data := &proto.GetGpioRsp{
		PWR: pwr,
	}
	rsp.OkRspWithData(c, data)
}

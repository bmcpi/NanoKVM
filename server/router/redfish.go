package router

import (
	"github.com/BMCPi/NanoKVM/server/middleware"
	"github.com/BMCPi/NanoKVM/server/service/redfish"

	"github.com/gin-gonic/gin"
)

func redfishRouter(r *gin.Engine) {
	service := redfish.NewService()

	// Public endpoints
	r.GET("/redfish", service.GetRedfishBase)
	r.GET("/redfish/v1", service.GetServiceRoot)
	r.GET("/redfish/v1/SessionService", service.GetSessionService)
	r.POST("/redfish/v1/SessionService/Sessions", service.CreateSession)

	// OpenAPI documentation — public so clients (bmclib, gofish, etc.)
	// can introspect the surface before authenticating. The rendered
	// human-readable docs page lives at /docs (behind auth, sharing the
	// dashboard chrome); see router.apiDocsHandler.
	r.GET("/redfish/v1/openapi.yaml", service.GetOpenAPIYAML)
	r.GET("/redfish/v1/openapi.json", service.GetOpenAPIJSON)

	// Protected endpoints
	api := r.Group("/redfish/v1").Use(middleware.CheckToken())
	{
		// Systems
		api.GET("/Systems", service.GetSystemCollection)
		api.GET("/Systems/1", service.GetSystem)
		api.POST("/Systems/1/Actions/ComputerSystem.Reset", service.ResetSystem)
		api.PATCH("/Systems/1", service.PatchSystem)

		// Bios — RPi 5 bootloader EEPROM as Redfish Bios.Attributes.
		// Live values come from eeprom.txt (U-Boot's per-boot dump);
		// PATCH /Bios/Settings stages a pieeprom.upd for the host's
		// rpi-eeprom-update to flash on next boot.
		api.GET("/Systems/1/Bios", service.GetBios)
		api.GET("/Systems/1/Bios/Settings", service.GetBiosSettings)
		api.PATCH("/Systems/1/Bios/Settings", service.PatchBiosSettings)
		api.GET("/Systems/1/Bios/AttributeRegistry", service.GetBiosAttributeRegistry)

		// Managers
		api.GET("/Managers", service.GetManagerCollection)
		api.GET("/Managers/1", service.GetManager)

		// Serial Interfaces
		api.GET("/Managers/1/SerialInterfaces", service.GetSerialInterfaceCollection)
		api.GET("/Managers/1/SerialInterfaces/1", service.GetSerialInterface)
		api.PATCH("/Managers/1/SerialInterfaces/1", service.PatchSerialInterface)

		// Virtual Media
		api.GET("/Managers/1/VirtualMedia", service.GetVirtualMediaCollection)
		api.GET("/Managers/1/VirtualMedia/1", service.GetVirtualMedia)
		api.POST("/Managers/1/VirtualMedia/1/Actions/VirtualMedia.InsertMedia", service.InsertMedia)
		api.POST("/Managers/1/VirtualMedia/1/Actions/VirtualMedia.EjectMedia", service.EjectMedia)

		// Sessions
		api.GET("/SessionService/Sessions", service.GetSessionCollection)
		api.DELETE("/SessionService/Sessions/:id", service.DeleteSession)

		// UpdateService (firmware updates)
		api.GET("/UpdateService", service.GetUpdateService)
		api.GET("/UpdateService/FirmwareInventory", service.GetFirmwareInventoryCollection)
		api.GET("/UpdateService/FirmwareInventory/BIOS", service.GetFirmwareInventoryUBoot)
		api.POST("/UpdateService/Actions/UpdateService.SimpleUpdate", service.SimpleUpdate)
		api.POST("/UpdateService/Actions/UpdateService.StartUpdate", service.StartUpdate)
	}
}

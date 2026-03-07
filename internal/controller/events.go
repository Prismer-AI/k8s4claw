package controller

// Event reasons for Claw lifecycle transitions.
const (
	EventClawProvisioning  = "ClawProvisioning"
	EventClawRunning       = "ClawRunning"
	EventClawDegraded      = "ClawDegraded"
	EventResourceCreated   = "ResourceCreated"
	EventSecretRotated     = "SecretRotated"
	EventReconcileError    = "ReconcileError"
	EventSelfConfigApplied = "SelfConfigApplied"
	EventSelfConfigDenied  = "SelfConfigDenied"

	EventAutoUpdateAvailable   = "AutoUpdateAvailable"
	EventAutoUpdateStarting    = "AutoUpdateStarting"
	EventAutoUpdateComplete    = "AutoUpdateComplete"
	EventAutoUpdateRollback    = "AutoUpdateRollback"
	EventAutoUpdateCircuitOpen = "AutoUpdateCircuitOpen"
)

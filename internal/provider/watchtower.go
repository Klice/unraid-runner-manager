package provider

const (
	WatchtowerEnableLabel     = "com.centurylinklabs.watchtower.enable"
	WatchtowerPreUpdateLabel  = "com.centurylinklabs.watchtower.lifecycle.pre-update"
	WatchtowerStopSignalLabel = "com.centurylinklabs.watchtower.stop-signal"
)

func WatchtowerSkipWhileBusy(probe string) map[string]string {
	return map[string]string{
		WatchtowerEnableLabel:    "true",
		WatchtowerPreUpdateLabel: probe + " && exit 75 || exit 0",
	}
}

func WatchtowerGracefulStop(signal string) map[string]string {
	return map[string]string{
		WatchtowerEnableLabel:     "true",
		WatchtowerStopSignalLabel: signal,
	}
}

//go:build !linux

package controller

const nativePhaseTransportAvailable = false

func nativePhasePipeIdentity(int) (*NativePhasePipeID, bool)          { return nil, false }
func nativePhaseEnlargePipe(int)                                      {}
func nativePhaseOpenWriter(int, NativePhasePipeID) *NativePhaseWriter { return nil }
func nativePhaseWrite(int, []byte) (int, bool)                        { return 0, false }
func nativePhaseClose(int)                                            {}

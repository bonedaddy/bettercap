package hid_recon

import (
	"fmt"
	"sync"
	"time"

	"github.com/bettercap/bettercap/modules/utils"
	"github.com/bettercap/bettercap/network"
	"github.com/bettercap/bettercap/session"

	"github.com/bettercap/nrf24"

	"github.com/evilsocket/islazy/tui"
)

type HIDRecon struct {
	session.SessionModule
	dongle       *nrf24.Dongle
	waitGroup    *sync.WaitGroup
	channel      int
	hopPeriod    time.Duration
	pingPeriod   time.Duration
	sniffPeriod  time.Duration
	lastHop      time.Time
	lastPing     time.Time
	useLNA       bool
	sniffLock    *sync.Mutex
	sniffAddrRaw []byte
	sniffAddr    string
	pingPayload  []byte
	inSniffMode  bool
	inPromMode   bool
	keyMap       string
	selector     *utils.ViewSelector
}

func NewHIDRecon(s *session.Session) *HIDRecon {
	mod := &HIDRecon{
		SessionModule: session.NewSessionModule("hid.recon", s),
		waitGroup:     &sync.WaitGroup{},
		sniffLock:     &sync.Mutex{},
		hopPeriod:     100 * time.Millisecond,
		pingPeriod:    100 * time.Millisecond,
		sniffPeriod:   500 * time.Millisecond,
		lastHop:       time.Now(),
		lastPing:      time.Now(),
		useLNA:        true,
		channel:       1,
		sniffAddrRaw:  nil,
		sniffAddr:     "",
		inSniffMode:   false,
		inPromMode:    false,
		pingPayload:   []byte{0x0f, 0x0f, 0x0f, 0x0f},
		keyMap:        "us",
	}

	mod.AddHandler(session.NewModuleHandler("hid.recon on", "",
		"Start HID recon.",
		func(args []string) error {
			return mod.Start()
		}))

	mod.AddHandler(session.NewModuleHandler("hid.recon off", "",
		"Stop HID recon.",
		func(args []string) error {
			return mod.Stop()
		}))

	sniff := session.NewModuleHandler("hid.sniff ADDRESS", `(?i)^hid\.sniff ([a-f0-9]{2}:[a-f0-9]{2}:[a-f0-9]{2}:[a-f0-9]{2}:[a-f0-9]{2}|clear)$`,
		"TODO TODO",
		func(args []string) error {
			return mod.setSniffMode(args[0])
		})

	sniff.Complete("hid.sniff", s.HIDCompleter)

	mod.AddHandler(sniff)

	mod.AddHandler(session.NewModuleHandler("hid.show", "",
		"TODO TODO",
		func(args []string) error {
			return mod.Show()
		}))

	mod.selector = utils.ViewSelectorFor(&mod.SessionModule, "hid.show", []string{"mac", "seen"}, "mac desc")

	return mod
}

func (mod HIDRecon) Name() string {
	return "hid.recon"
}

func (mod HIDRecon) Description() string {
	return "TODO TODO"
}

func (mod HIDRecon) Author() string {
	return "Simone Margaritelli <evilsocket@gmail.com>"
}

func (mod *HIDRecon) Configure() error {
	var err error

	if mod.dongle, err = nrf24.Open(); err != nil {
		return err
	}

	mod.Debug("using device %s", mod.dongle.String())

	if mod.useLNA {
		if err = mod.dongle.EnableLNA(); err != nil {
			return err
		}
		mod.Debug("LNA enabled")
	}

	return nil
}

func (mod *HIDRecon) setSniffMode(mode string) error {
	mod.sniffLock.Lock()
	defer mod.sniffLock.Unlock()

	mod.inSniffMode = false
	if mode == "clear" {
		mod.Debug("restoring recon mode")
		mod.sniffAddrRaw = nil
		mod.sniffAddr = ""
	} else {
		if err, raw := nrf24.ConvertAddress(mode); err != nil {
			return err
		} else {
			mod.Info("sniffing device %s ...", tui.Bold(mode))
			mod.sniffAddr = network.NormalizeHIDAddress(mode)
			mod.sniffAddrRaw = raw
		}
	}
	return nil
}

func (mod *HIDRecon) isSniffing() bool {
	mod.sniffLock.Lock()
	defer mod.sniffLock.Unlock()
	return mod.sniffAddrRaw != nil
}

func (mod *HIDRecon) doHopping() {
	if mod.inPromMode == false {
		if err := mod.dongle.EnterPromiscMode(); err != nil {
			mod.Error("error entering promiscuous mode: %v", err)
		} else {
			mod.inSniffMode = false
			mod.inPromMode = true
			mod.Info("device entered promiscuous mode")
		}
	}

	if time.Since(mod.lastHop) >= mod.hopPeriod {
		mod.channel++
		if mod.channel > nrf24.TopChannel {
			mod.channel = 1
		}
		if err := mod.dongle.SetChannel(mod.channel); err != nil {
			mod.Warning("error hopping on channel %d: %v", mod.channel, err)
		} else {
			mod.lastHop = time.Now()
		}
	}
}

func (mod *HIDRecon) doPing() {
	if mod.inSniffMode == false {
		if err := mod.dongle.EnterSnifferModeFor(mod.sniffAddrRaw); err != nil {
			mod.Error("error entering sniffer mode for %s: %v", mod.sniffAddr, err)
		} else {
			mod.inSniffMode = true
			mod.inPromMode = false
			mod.Info("device entered sniffer mode for %s", mod.sniffAddr)
		}
	}

	if time.Since(mod.lastPing) >= mod.pingPeriod {
		// try on the current channel first
		if err := mod.dongle.TransmitPayload(mod.pingPayload, 250, 1); err != nil {
			for mod.channel = 1; mod.channel <= nrf24.TopChannel; mod.channel++ {
				if err := mod.dongle.SetChannel(mod.channel); err != nil {
					mod.Error("error setting channel %d: %v", mod.channel, err)
				} else if err = mod.dongle.TransmitPayload(mod.pingPayload, 250, 1); err == nil {
					mod.lastPing = time.Now()
					return
				}
			}
		}
	}

	dev, found := mod.Session.HID.Get(mod.sniffAddr)
	if found == false {
		mod.Warning("could not find HID device %s", mod.sniffAddr)
		return
	}

	builder, found := FrameBuilders[dev.Type]
	if found == false {
		mod.Warning("HID frame injection is not supported for device type %s", dev.Type.String())
		return
	}

	str := "hello world"
	cmds := make([]Command, 0)
	keyMap := KeyMapFor(mod.keyMap)
	if keyMap == nil {
		mod.Warning("could not find keymap for '%s' layout", mod.keyMap)
		return
	}

	for _, c := range str {
		ch := fmt.Sprintf("%c", c)
		if m, found := keyMap[ch]; found {
			cmds = append(cmds, Command{
				Char: ch,
				HID:  m.HID,
				Mode: m.Mode,
			})
		} else {
			mod.Warning("could not find HID command for '%c'", ch)
			return
		}
	}

	builder.BuildFrames(cmds)

	mod.Info("injecting %d HID commands ...", len(cmds))

	numFrames := 0
	szFrames := 0

	for i, cmd := range cmds {
		for j, frame := range cmd.Frames {
			numFrames++
			if err := mod.dongle.TransmitPayload(frame, 250, 1); err != nil {
				mod.Error("error sending frame #%d of HID command #%d: %v", j, i, err)
				return
			} else if cmd.Sleep > 0 {
				szFrames += len(frame)
				mod.Debug("sleeping %dms after frame #%d of command #%d ...", cmd.Sleep, j, i)
				time.Sleep(time.Duration(cmd.Sleep) * time.Millisecond)
			}
		}
	}

	mod.Info("send %d frames for %d bytes total", numFrames, szFrames)
}

func (mod *HIDRecon) onSniffedBuffer(buf []byte) {
	if sz := len(buf); sz > 0 && buf[0] == 0x00 {
		buf = buf[1:]
		mod.Debug("sniffed payload %x for %s", buf, mod.sniffAddr)
		if dev, found := mod.Session.HID.Get(mod.sniffAddr); found {
			dev.LastSeen = time.Now()
			dev.AddPayload(buf)
			dev.AddChannel(mod.channel)
		} else {
			mod.Warning("got a payload for unknown device %s", mod.sniffAddr)
		}
	}
}

func (mod *HIDRecon) onDeviceDetected(buf []byte) {
	if sz := len(buf); sz >= 5 {
		addr, payload := buf[0:5], buf[5:]
		mod.Debug("detected device %x on channel %d (payload:%x)\n", addr, mod.channel, payload)
		if isNew, dev := mod.Session.HID.AddIfNew(addr, mod.channel, payload); isNew {
			// sniff for a while in order to detect the device type
			go func() {
				defer func() {
					mod.sniffLock.Unlock()
					mod.setSniffMode("clear")
				}()

				mod.setSniffMode(dev.Address)
				// make sure nobody can sniff to another
				// address until we're not done here...
				mod.sniffLock.Lock()

				time.Sleep(mod.sniffPeriod)
			}()
		}
	}
}

func (mod *HIDRecon) Start() error {
	if err := mod.Configure(); err != nil {
		return err
	}

	return mod.SetRunning(true, func() {
		mod.waitGroup.Add(1)
		defer mod.waitGroup.Done()

		mod.Info("hopping on %d channels every %s", nrf24.TopChannel, mod.hopPeriod)
		for mod.Running() {
			if mod.isSniffing() {
				mod.doPing()
			} else {
				mod.doHopping()
			}

			buf, err := mod.dongle.ReceivePayload()
			if err != nil {
				mod.Warning("error receiving payload from channel %d: %v", mod.channel, err)
				continue
			}

			if mod.isSniffing() {
				mod.onSniffedBuffer(buf)
			} else {
				mod.onDeviceDetected(buf)
			}
		}

		mod.Debug("stopped")
	})
}

func (mod *HIDRecon) Stop() error {
	return mod.SetRunning(false, func() {
		mod.waitGroup.Wait()
		mod.dongle.Close()
		mod.Debug("device closed")
	})
}
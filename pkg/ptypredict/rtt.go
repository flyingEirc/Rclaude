package ptypredict

import (
	"math"
	"time"
)

// sampleRTT derives an RTT sample from the newest stdin send covered by an
// echo ack: the ack implies the server held the input for echoTimeout before
// acknowledging, so that hold is subtracted (mosh measures RTT at the packet
// layer; the echo path is our equivalent).
func (e *Engine) sampleRTT(ackOffset uint64) {
	var latest offsetTime
	found := false
	for len(e.sendTimes) > 0 && e.sendTimes[0].offset <= ackOffset {
		latest = e.sendTimes[0]
		e.sendTimes = e.sendTimes[1:]
		found = true
	}
	if !found {
		return
	}
	sample := e.now().Sub(latest.at) - echoTimeout
	if sample < time.Millisecond {
		sample = time.Millisecond
	}
	if sample >= 5*time.Second {
		return // mosh: ignore absurd samples (server was stopped)
	}
	ms := float64(sample) / float64(time.Millisecond)
	if !e.srttValid {
		e.srtt = ms
		e.srttValid = true
	} else {
		e.srtt = 0.875*e.srtt + 0.125*ms // RFC 6298 alpha = 1/8
	}
	e.updateTriggers()
}

// sendInterval converts SRTT into mosh's send_interval equivalent:
// clamp(ceil(SRTT/2), 20, 250) milliseconds.
func (e *Engine) sendInterval() int {
	if !e.srttValid {
		return srttTriggerLowMs
	}
	iv := int(math.Ceil(e.srtt / 2))
	return clamp(iv, 20, 250)
}

// updateTriggers applies mosh's hysteresis for showing and flagging
// predictions.
func (e *Engine) updateTriggers() {
	iv := e.sendInterval()
	if iv > srttTriggerHighMs {
		e.srttTrigger = true
	} else if e.srttTrigger && iv <= srttTriggerLowMs && !e.anyActive() {
		// Don't let visible predictions vanish mid-word.
		e.srttTrigger = false
	}
	if iv > flagTriggerHighMs {
		e.flagging = true
	} else if e.flagging && iv <= flagTriggerLowMs {
		e.flagging = false
	}
	if e.glitchTrigger > glitchRepairCount {
		e.flagging = true
	}
}

func (e *Engine) anyActive() bool {
	return len(e.cells) > 0 || e.cursor.active
}

// repairGlitch heals the glitch trigger after a quick confirmation (mosh:
// ten fast confirmations at least 150ms apart).
func (e *Engine) repairGlitch(madeAt time.Time) {
	now := e.now()
	if e.glitchTrigger <= 0 {
		return
	}
	if now.Sub(madeAt) >= glitchThreshold {
		return
	}
	if now.Sub(e.lastQuickConfirm) < glitchRepairMinInterval {
		return
	}
	e.glitchTrigger--
	e.lastQuickConfirm = now
}

// escalateGlitch turns on prediction display (and eventually flagging) when
// predictions sit unconfirmed for too long.
func (e *Engine) escalateGlitch() {
	now := e.now()
	for i := range e.cells {
		age := now.Sub(e.cells[i].madeAt)
		switch {
		case age >= glitchFlagThreshold:
			e.glitchTrigger = 2 * glitchRepairCount
			e.flagging = true
		case age >= glitchThreshold && e.glitchTrigger < glitchRepairCount:
			e.glitchTrigger = glitchRepairCount
		}
	}
}

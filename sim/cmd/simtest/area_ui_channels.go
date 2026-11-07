package main

import (
	"math"
	"strings"

	"github.com/labeth/open-dso-core/uimodel"
)

// uiAnyChannel checks the features that used to be C1 and C2's on C3 and
// C4 of the injected signals (C3 a 2 MHz square, C4 a 2 MHz sine, each
// 1 V peak to peak): the frequency response between them, X-Y of the pair,
// SOUND's channel rows, the mask tolerance in divisions, and a scope with
// C1 switched off that still measures C2 to C4.
func uiAnyChannel(u *uiRig) {
	h := u.h
	for _, q := range []string{":TIM:SCAL 1E-6", "C3:TRA ON", "C4:TRA ON", "C3:VDIV 0.2", "C4:VDIV 0.2"} {
		_, _ = scpiQuery(u.ctx, q)
	}
	res := u.setRow("trig.edge", 4) // C3 Rise: the frames lock on the reference
	h.check("trig-c3", res.OK, "trig.edge C3 Rise: %+v", res)

	// Bode with its reference on C3 and the DUT on C4: the square's
	// fundamental is 4/pi of its half swing, the sine's its half swing, so
	// the response at 2 MHz is 20·log10(pi/4) = -2.1 dB
	r1, r2 := u.setRow("view.bode_ref", 2), u.setRow("view.bode_dut", 3)
	h.check("bode-rows", r1.OK && r2.OK, "view.bode_ref C3 %+v, view.bode_dut C4 %+v", r1, r2)
	var arm map[string]any
	err := u.post("/api/set", `{"control":"bodemode","value":1,"lo":2,"hi":3}`, &arm)
	h.check("bode-arm", err == nil && arm["ok"] != false, "bodemode on C3/C4: %v (%v)", arm, err)
	var curve struct {
		N     int       `json:"n"`
		Freq  []float64 `json:"freq"`
		Gain  []float64 `json:"gain_db"`
		Phase []float64 `json:"phase_deg"`
	}
	ok := u.until(func() bool {
		if u.get("/api/bode", &curve) != nil || curve.N == 0 {
			return false
		}
		for i, f := range curve.Freq {
			if math.Abs(f-2e6) < 0.05*2e6 && math.Abs(curve.Gain[i]+2.1) < 1.5 {
				return true
			}
		}
		return false
	})
	h.check("bode-c3-c4", ok, "the response C3 to C4: %v Hz, %v dB, %v deg (2 MHz, -2.1 dB expected)", curve.Freq, curve.Gain, curve.Phase)
	ref, _ := u.sheetRow("view.bode_ref")
	dut, _ := u.sheetRow("view.bode_dut")
	h.check("bode-rows-read", ref.Text == "C3" && dut.Text == "C4", "the sheet's Bode rows: ref %q, DUT %q", ref.Text, dut.Text)
	_ = u.post("/api/set", `{"control":"bodemode","value":0,"lo":2,"hi":3}`, &arm) // disarmed again; its reply is not checked

	// X-Y of C3 against C4, on the workspace (no sheet over the plot)
	if u.panel().Mode != modeWorkspace {
		u.ctl(keyMenu)
		u.until(func() bool { return u.panel().Mode == modeWorkspace })
	}
	ytImg, _ := u.shot("ui-yt.png")
	rx, ry := u.setRow("view.xy_x", 2), u.setRow("view.xy_y", 3)
	rv := u.setRow("view.view", 1)
	h.check("xy-rows", rx.OK && ry.OK && rv.OK, "view.xy_x C3 %+v, view.xy_y C4 %+v, view X-Y %+v", rx, ry, rv)
	h.check("xy-view", u.until(func() bool { return u.panel().View == "X-Y" }), "the view %q", u.panel().View)
	xyImg, _ := u.shot("ui-xy.png")
	n := differs(ytImg, xyImg)
	h.check("xy-c3-c4-fb0", n > 1000, "X-Y of C3 and C4 changed %d pixels of fb0", n)
	_ = u.setRow("view.view", 0) // Y-T again; its reply is not checked

	// SOUND's channel rows offer every channel
	sc := u.setRow("snd.ch", 3)
	ab := u.setRow("snd.ab_in", 2)
	ch, _ := u.sheetRow("snd.ch")
	h.check("sound-rows-c3-c4", sc.OK && ab.OK && ch.Text == "CH4" && len(ch.Options) == 4, "snd.ch %+v (%q, %d options), snd.ab_in %+v", sc, ch.Text, len(ch.Options), ab)

	// the mask's tolerance in divisions on the 12-bit converter's scale
	tol, _ := u.sheetRow("cmp.mask_tol")
	h.check("mask-tol-div", strings.HasSuffix(tol.Text, "div"), "the mask tolerance on C3 reads %q", tol.Text)

	// C1 switched off: C3 and C4 are still measured. C2 is switched on too
	// (the default state has C1 alone) and reported: SCPI measures it from
	// the frames, the UI model's value for it stays empty (open: C2 alone in
	// its converter pair)
	on2 := u.setRow("ch2.on", 1)
	h.check("ch2-on", on2.OK, "ch2.on On: %+v", on2)
	off := u.setRow("ch1.on", 0)
	meas := u.setRow("meas.freq", 15) // all four
	h.check("c1-off-rows", off.OK && meas.OK, "ch1.on Off %+v, meas.freq All %+v", off, meas)
	var freq uimodel.RowModel
	ok = u.until(func() bool {
		freq, _ = u.sheetRow("meas.freq")
		return len(freq.Live) == 4 && freq.Live[0] == "" && nearSI(freq.Live[2], 2e6, 0.02) && nearSI(freq.Live[3], 2e6, 0.02)
	})
	c2, _ := u.sheetRow("ch2.on")
	tra, _ := scpiQuery(u.ctx, "C1:TRA?;C2:TRA?")
	m2, _ := scpiQuery(u.ctx, ":MEAS:SIMP:SOUR C2;:MEAS:SIMP:VAL? FREQ")
	pst := u.panel()
	h.check("c1-off-measures", ok, "with C1 off the frequencies read %q (C3 and C4 2 MHz injected; C2 1 MHz); ch2.on %q, C1/C2:TRA? %q, C2 over SCPI %q, shown %v, workspace %s", freq.Live, c2.Text, tra, m2, pst.Display.ShowCh, pst.Workspace)
	// the default state again (C1 alone, the trigger on it): the saver keeps
	// what a run leaves for the next run's start
	for _, q := range []string{"C1:TRA ON", ":TRIG:EDGE:SOUR C1", "C2:TRA OFF", "C3:TRA OFF", "C4:TRA OFF"} {
		_, _ = scpiQuery(u.ctx, q)
	}
}

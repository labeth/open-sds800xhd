# Hardware frequency counter

The native readout bar and web header show the dedicated PL frequency counter as `Counter Cn` followed by its reading in Hz, kHz, MHz or GHz. This is independent of the frequency estimated from the displayed waveform. A board without a hardware counter shows no counter readout.

The acquisition owner services the counter while running, stopped and waiting for a trigger. Invalid readings and read failures show `--`. The source label comes from the counter's actual source selection; it can differ from a newly staged trigger source until the next arm, and some trigger modes preserve another source. A source change starts a fresh measurement gate rather than relabeling the old reading.

The board register model, source switching and UI integration are tested in simulation. The counter's reference-clock calibration, absolute scale and accuracy have not been verified on a real SDS800X HD. Displayed digits do not establish calibrated accuracy. No counter calibration or reference-clock trim procedure is implemented.

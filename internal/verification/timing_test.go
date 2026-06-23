package verification

import "testing"

func TestAnalyzeTimingProfile_Empty(t *testing.T) {
	p := AnalyzeTimingProfile(nil)
	if p.Count != 0 || p.HumanLikelihood != 0 {
		t.Fatalf("empty profile = %+v", p)
	}
}

func TestHumanLikelihood(t *testing.T) {
	// Bot-like: fast and consistent.
	bot := AnalyzeTimingProfile([]int{80, 95, 110, 90, 100, 85, 120, 95})
	if bot.HumanLikelihood > 0.3 {
		t.Errorf("bot likelihood too high: %.2f (%+v)", bot.HumanLikelihood, bot)
	}
	// Human-like: slow and variable.
	human := AnalyzeTimingProfile([]int{2200, 6000, 3500, 8000, 1800, 5200, 7000, 2600})
	if human.HumanLikelihood < 0.6 {
		t.Errorf("human likelihood too low: %.2f (%+v)", human.HumanLikelihood, human)
	}
	if human.HumanLikelihood <= bot.HumanLikelihood {
		t.Errorf("human (%.2f) should score higher than bot (%.2f)", human.HumanLikelihood, bot.HumanLikelihood)
	}
}

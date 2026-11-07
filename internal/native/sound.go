package native

import (
	"math"
	"sync"

	"github.com/egoist/mygo/audio"
)

type chimeNote struct {
	frequency float64
	onset     float64
	level     float64
}

type chimePartial struct {
	multiple float64
	level    float64
	decay    float64
}

const (
	soundSampleRate = 44100
	soundDuration   = 0.7
	soundAttack     = 0.006
	soundRelease    = 0.08
	soundVolume     = 0.35

	soundAttentionVolume = 0.38
	soundDormantVolume   = 0.18
	soundDormantDuration = 0.32
)

// FinishChimeSamples 生成完成提示音的单声道 PCM 样本（采样率与 float32 切片）。
// 算法：上升五度（D5 -> A5），
// 辅以柔和包络与谐波衰减。
func FinishChimeSamples() (sampleRate int, samples []float32) {
	sr := soundSampleRate
	duration := soundDuration
	count := int(math.Round(float64(sr) * duration))
	raw := make([]float64, count)

	notes := []chimeNote{
		{frequency: 587.33, onset: 0.0, level: 0.8},
		{frequency: 880.0, onset: 0.14, level: 1.0},
	}
	partials := []chimePartial{
		{multiple: 1.0, level: 1.0, decay: 0.14},
		{multiple: 2.0, level: 0.22, decay: 0.06},
		{multiple: 4.0, level: 0.08, decay: 0.04},
	}

	for _, n := range notes {
		start := int(math.Round(n.onset * float64(sr)))
		for index := start; index < count; index++ {
			time := float64(index-start) / float64(sr)
			var onset float64
			if time < soundAttack {
				onset = 0.5 - 0.5*math.Cos(math.Pi*time/soundAttack)
			} else {
				onset = 1.0
			}
			var tone float64
			for _, p := range partials {
				tone += p.level * math.Exp(-time/p.decay) * math.Sin(2*math.Pi*n.frequency*p.multiple*time)
			}
			raw[index] += n.level * onset * tone
		}
	}

	fade := int(math.Round(soundRelease * float64(sr)))
	for index := count - fade; index < count; index++ {
		raw[index] *= float64(count-index) / float64(fade)
	}

	peak := 0.0
	for _, s := range raw {
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}

	scale := 0.0
	if peak > 0 {
		scale = (0.9 / peak) * soundVolume
	}

	samples = make([]float32, count)
	for i, s := range raw {
		samples[i] = float32(s * scale)
	}

	return sr, samples
}

// AttentionChimeSamples 生成需要用户介入或审批时的专属提示音样本。
// 算法：上升四度（G5 -> C6）。
func AttentionChimeSamples() (sampleRate int, samples []float32) {
	sr := soundSampleRate
	duration := soundDuration
	count := int(math.Round(float64(sr) * duration))
	raw := make([]float64, count)

	notes := []chimeNote{
		{frequency: 783.99, onset: 0.0, level: 0.8},
		{frequency: 1046.50, onset: 0.14, level: 1.0},
	}
	partials := []chimePartial{
		{multiple: 1.0, level: 1.0, decay: 0.14},
		{multiple: 2.0, level: 0.22, decay: 0.06},
		{multiple: 4.0, level: 0.08, decay: 0.04},
	}

	for _, n := range notes {
		start := int(math.Round(n.onset * float64(sr)))
		for index := start; index < count; index++ {
			time := float64(index-start) / float64(sr)
			var onset float64
			if time < soundAttack {
				onset = 0.5 - 0.5*math.Cos(math.Pi*time/soundAttack)
			} else {
				onset = 1.0
			}
			var tone float64
			for _, p := range partials {
				tone += p.level * math.Exp(-time/p.decay) * math.Sin(2*math.Pi*n.frequency*p.multiple*time)
			}
			raw[index] += n.level * onset * tone
		}
	}

	fade := int(math.Round(soundRelease * float64(sr)))
	for index := count - fade; index < count; index++ {
		raw[index] *= float64(count-index) / float64(fade)
	}

	peak := 0.0
	for _, s := range raw {
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}

	scale := 0.0
	if peak > 0 {
		scale = (0.9 / peak) * soundAttentionVolume
	}

	samples = make([]float32, count)
	for i, s := range raw {
		samples[i] = float32(s * scale)
	}

	return sr, samples
}

// DormantChimeSamples 生成会话休眠提示音样本。
// 算法：下行三度（E5 -> C5），短促木质声音。
func DormantChimeSamples() (sampleRate int, samples []float32) {
	sr := soundSampleRate
	duration := soundDormantDuration
	count := int(math.Round(float64(sr) * duration))
	raw := make([]float64, count)

	notes := []chimeNote{
		{frequency: 659.26, onset: 0.0, level: 0.7},
		{frequency: 523.25, onset: 0.075, level: 1.0},
	}
	partials := []chimePartial{
		{multiple: 1.0, level: 1.0, decay: 0.075},
		{multiple: 3.0, level: 0.16, decay: 0.02},
		{multiple: 2.0, level: 0.08, decay: 0.03},
	}

	for _, n := range notes {
		start := int(math.Round(n.onset * float64(sr)))
		for index := start; index < count; index++ {
			time := float64(index-start) / float64(sr)
			var onset float64
			if time < soundAttack {
				onset = 0.5 - 0.5*math.Cos(math.Pi*time/soundAttack)
			} else {
				onset = 1.0
			}
			var tone float64
			for _, p := range partials {
				tone += p.level * math.Exp(-time/p.decay) * math.Sin(2*math.Pi*n.frequency*p.multiple*time)
			}
			raw[index] += n.level * onset * tone
		}
	}

	fade := int(math.Round(soundRelease * float64(sr)))
	for index := count - fade; index < count; index++ {
		raw[index] *= float64(count-index) / float64(fade)
	}

	peak := 0.0
	for _, s := range raw {
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}

	scale := 0.0
	if peak > 0 {
		scale = (0.9 / peak) * soundDormantVolume
	}

	samples = make([]float32, count)
	for i, s := range raw {
		samples[i] = float32(s * scale)
	}

	return sr, samples
}

var (
	chimePlayer *audio.Player
	chimeMu     sync.Mutex

	attentionPlayer *audio.Player
	attentionMu     sync.Mutex

	dormantPlayer *audio.Player
	dormantMu     sync.Mutex
)

// PlayFinishChime 播放会话完成提示音。
// 复用同一播放器，支持重播，正在播放中时不叠加。
func PlayFinishChime() {
	chimeMu.Lock()
	defer chimeMu.Unlock()
	if chimePlayer == nil {
		sr, samples := FinishChimeSamples()
		chimePlayer = audio.NewSound(sr, 1, samples).NewPlayer()
	} else if chimePlayer.Playing() {
		return
	}
	chimePlayer.Seek(0)
	chimePlayer.Play()
}

// PlayAttentionChime 播放需要用户介入（审批或提问）的专属提示音。
// 复用同一播放器，支持重播，正在播放中时不叠加。
func PlayAttentionChime() {
	attentionMu.Lock()
	defer attentionMu.Unlock()
	if attentionPlayer == nil {
		sr, samples := AttentionChimeSamples()
		attentionPlayer = audio.NewSound(sr, 1, samples).NewPlayer()
	} else if attentionPlayer.Playing() {
		return
	}
	attentionPlayer.Seek(0)
	attentionPlayer.Play()
}

// PlayDormantChime 播放会话休眠提示音。
func PlayDormantChime() {
	dormantMu.Lock()
	defer dormantMu.Unlock()
	if dormantPlayer == nil {
		sr, samples := DormantChimeSamples()
		dormantPlayer = audio.NewSound(sr, 1, samples).NewPlayer()
	}
	dormantPlayer.Seek(0)
	dormantPlayer.Play()
}

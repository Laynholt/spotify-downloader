package backend

import (
	"fmt"
	"math"
	"strings"
)

type QualityAssessment struct {
	Format               string  `json:"format"`
	DeclaredBitrateKbps  int     `json:"declared_bitrate_kbps,omitempty"`
	EstimatedBitrateKbps int     `json:"estimated_bitrate_kbps,omitempty"`
	CutoffFrequencyHz    float64 `json:"cutoff_frequency_hz,omitempty"`
	Confidence           float64 `json:"confidence"`
	VerdictCode          string  `json:"verdict_code"`
	Verdict              string  `json:"verdict"`
	Details              string  `json:"details"`
}

func assessSpectralQuality(format string, declaredBitrateKbps int, spectrum *SpectrumData) *QualityAssessment {
	normalizedFormat := strings.ToLower(strings.TrimPrefix(format, "."))
	assessment := &QualityAssessment{
		Format:              normalizedFormat,
		DeclaredBitrateKbps: declaredBitrateKbps,
		VerdictCode:         "inconclusive",
		Verdict:             "Inconclusive",
		Details:             "Not enough spectrum data to estimate source quality.",
	}

	if spectrum == nil || len(spectrum.TimeSlices) == 0 || spectrum.FreqBins == 0 || spectrum.MaxFreq <= 0 {
		return assessment
	}

	cutoffHz := estimateHighFrequencyCutoff(spectrum)
	estimatedKbps := estimateBitrateTierFromCutoff(cutoffHz)
	confidence := estimateQualityConfidence(cutoffHz, declaredBitrateKbps, estimatedKbps)

	assessment.CutoffFrequencyHz = cutoffHz
	assessment.EstimatedBitrateKbps = estimatedKbps
	assessment.Confidence = confidence

	if normalizedFormat == "flac" {
		if cutoffHz > 0 && cutoffHz < 18000 {
			assessment.VerdictCode = "spectrum_limited"
			assessment.Verdict = "Spectrum limited"
			assessment.Details = fmt.Sprintf("FLAC container, but useful high-frequency content appears to stop around %.1f kHz.", cutoffHz/1000)
			return assessment
		}
		assessment.VerdictCode = "lossless_container"
		assessment.Verdict = "Lossless container"
		assessment.Details = fmt.Sprintf("FLAC container with useful content up to about %.1f kHz.", cutoffHz/1000)
		return assessment
	}

	if normalizedFormat != "mp3" {
		assessment.Details = fmt.Sprintf("Spectrum cutoff is about %.1f kHz.", cutoffHz/1000)
		return assessment
	}

	if declaredBitrateKbps <= 0 {
		assessment.Details = fmt.Sprintf("MP3 bitrate metadata is unavailable; spectrum suggests roughly %d kbps source tier.", estimatedKbps)
		return assessment
	}

	if declaredBitrateKbps >= 300 && estimatedKbps <= 128 {
		assessment.VerdictCode = "suspicious"
		assessment.Verdict = "Suspicious upsample"
		assessment.Details = fmt.Sprintf("Declared %d kbps MP3, but the spectrum rolls off near %.1f kHz, typical of a lower-bitrate source.", declaredBitrateKbps, cutoffHz/1000)
		return assessment
	}

	if declaredBitrateKbps >= 300 && estimatedKbps == 192 {
		assessment.VerdictCode = "inconclusive"
		assessment.Verdict = "Possibly transcoded"
		assessment.Details = fmt.Sprintf("Declared %d kbps MP3, but the spectrum stops around %.1f kHz. This can be a transcode or a naturally bandwidth-limited master.", declaredBitrateKbps, cutoffHz/1000)
		return assessment
	}

	if estimatedKbps+32 < declaredBitrateKbps && confidence >= 0.7 {
		assessment.VerdictCode = "suspicious"
		assessment.Verdict = "Suspicious bitrate"
		assessment.Details = fmt.Sprintf("Declared %d kbps MP3, while spectral content is closer to %d kbps.", declaredBitrateKbps, estimatedKbps)
		return assessment
	}

	assessment.VerdictCode = "likely_genuine"
	assessment.Verdict = "Likely genuine"
	assessment.Details = fmt.Sprintf("Spectrum extends to about %.1f kHz, consistent with the declared MP3 bitrate.", cutoffHz/1000)
	return assessment
}

func estimateHighFrequencyCutoff(spectrum *SpectrumData) float64 {
	if spectrum == nil || len(spectrum.TimeSlices) == 0 || spectrum.FreqBins == 0 || spectrum.MaxFreq <= 0 {
		return 0
	}

	bins := spectrum.FreqBins
	averages := make([]float64, bins)
	counts := make([]int, bins)
	for _, slice := range spectrum.TimeSlices {
		for i := 0; i < bins && i < len(slice.Magnitudes); i++ {
			if !math.IsNaN(slice.Magnitudes[i]) && !math.IsInf(slice.Magnitudes[i], 0) {
				averages[i] += slice.Magnitudes[i]
				counts[i]++
			}
		}
	}

	maxMagnitude := math.Inf(-1)
	for i := range averages {
		if counts[i] == 0 {
			averages[i] = -120
			continue
		}
		averages[i] /= float64(counts[i])
		if averages[i] > maxMagnitude {
			maxMagnitude = averages[i]
		}
	}
	if math.IsInf(maxMagnitude, -1) {
		return 0
	}

	threshold := maxMagnitude - 45
	minFreq := 11000.0
	cutoffBin := 0
	for i := range averages {
		freq := float64(i) * spectrum.MaxFreq / float64(max(1, bins-1))
		if freq >= minFreq && averages[i] >= threshold {
			cutoffBin = i
		}
	}

	return float64(cutoffBin) * spectrum.MaxFreq / float64(max(1, bins-1))
}

func estimateBitrateTierFromCutoff(cutoffHz float64) int {
	switch {
	case cutoffHz <= 0:
		return 0
	case cutoffHz < 16500:
		return 128
	case cutoffHz < 18800:
		return 192
	case cutoffHz < 19800:
		return 256
	default:
		return 320
	}
}

func estimateQualityConfidence(cutoffHz float64, declaredKbps int, estimatedKbps int) float64 {
	if cutoffHz <= 0 || estimatedKbps <= 0 {
		return 0
	}

	confidence := 0.55
	if declaredKbps > 0 {
		gap := declaredKbps - estimatedKbps
		switch {
		case gap >= 160:
			confidence = 0.88
		case gap >= 96:
			confidence = 0.76
		case gap >= 48:
			confidence = 0.64
		default:
			confidence = 0.68
		}
	}

	distanceToBoundary := minFloat(
		math.Abs(cutoffHz-16500),
		math.Abs(cutoffHz-18800),
		math.Abs(cutoffHz-19800),
	)
	if distanceToBoundary < 350 {
		confidence -= 0.12
	}

	return math.Max(0, math.Min(0.98, confidence))
}

func minFloat(first float64, rest ...float64) float64 {
	result := first
	for _, value := range rest {
		if value < result {
			result = value
		}
	}
	return result
}

package backend

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/cmplx"
	"os/exec"
	"strconv"
)

type SpectrumData struct {
	TimeSlices []TimeSlice `json:"time_slices"`
	SampleRate int         `json:"sample_rate"`
	FreqBins   int         `json:"freq_bins"`
	Duration   float64     `json:"duration"`
	MaxFreq    float64     `json:"max_freq"`
}

type TimeSlice struct {
	Time       float64   `json:"time"`
	Magnitudes []float64 `json:"magnitudes"`
}

func AnalyzeSpectrum(filepath string) (*SpectrumData, error) {
	metadata, err := GetMetadataWithFFprobe(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata for spectrum: %w", err)
	}

	sampleRate := int(metadata.SampleRate)
	if sampleRate <= 0 {
		sampleRate = 44100
	}

	samples, err := decodeMonoPCMWithFFmpeg(filepath, sampleRate)
	if err != nil {
		return nil, err
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("no audio samples found")
	}

	return calculateSpectrum(samples, sampleRate), nil
}

func decodeMonoPCMWithFFmpeg(filepath string, sampleRate int) ([]float64, error) {
	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get ffmpeg path: %w", err)
	}

	maxSamples := 10 * 1024 * 1024
	maxSeconds := float64(maxSamples) / float64(sampleRate)
	args := []string{
		"-v", "error",
		"-i", filepath,
		"-vn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-t", fmt.Sprintf("%.3f", maxSeconds),
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"pipe:1",
	}

	cmd := exec.Command(ffmpegPath, args...)
	setHideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffmpeg spectrum decode failed: %v - %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("ffmpeg spectrum decode failed: %w", err)
	}

	sampleCount := len(output) / 2
	if sampleCount > maxSamples {
		sampleCount = maxSamples
	}

	samples := make([]float64, sampleCount)
	for i := 0; i < sampleCount; i++ {
		raw := int16(binary.LittleEndian.Uint16(output[i*2 : i*2+2]))
		samples[i] = float64(raw) / 32768.0
	}

	return samples, nil
}

func calculateSpectrum(samples []float64, sampleRate int) *SpectrumData {
	fftSize := 8192
	numTimeSlices := 300

	duration := float64(len(samples)) / float64(sampleRate)

	samplesPerSlice := len(samples) / numTimeSlices
	if samplesPerSlice < fftSize {
		samplesPerSlice = fftSize
		numTimeSlices = len(samples) / fftSize
	}

	timeSlices := make([]TimeSlice, 0, numTimeSlices)
	freqBins := fftSize / 2
	maxFreq := float64(sampleRate) / 2.0

	for i := 0; i < numTimeSlices; i++ {
		startIdx := i * samplesPerSlice
		if startIdx+fftSize > len(samples) {
			break
		}

		window := samples[startIdx : startIdx+fftSize]
		windowedSamples := applyHannWindow(window)
		spectrum := fft(windowedSamples)

		magnitudes := make([]float64, freqBins)
		for j := 0; j < freqBins; j++ {
			magnitude := cmplx.Abs(spectrum[j])
			if magnitude < 1e-10 {
				magnitude = 1e-10
			}
			magnitudes[j] = 20 * math.Log10(magnitude)
		}

		timeSlice := TimeSlice{
			Time:       float64(startIdx) / float64(sampleRate),
			Magnitudes: magnitudes,
		}
		timeSlices = append(timeSlices, timeSlice)
	}

	return &SpectrumData{
		TimeSlices: timeSlices,
		SampleRate: sampleRate,
		FreqBins:   freqBins,
		Duration:   duration,
		MaxFreq:    maxFreq,
	}
}

func applyHannWindow(samples []float64) []float64 {
	n := len(samples)
	windowed := make([]float64, n)

	for i := 0; i < n; i++ {
		window := 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(n-1)))
		windowed[i] = samples[i] * window
	}

	return windowed
}

func fft(samples []float64) []complex128 {
	n := len(samples)
	x := make([]complex128, n)
	for i := 0; i < n; i++ {
		x[i] = complex(samples[i], 0)
	}
	return fftRecursive(x)
}

func fftRecursive(x []complex128) []complex128 {
	n := len(x)

	if n <= 1 {
		return x
	}

	even := make([]complex128, n/2)
	odd := make([]complex128, n/2)

	for i := 0; i < n/2; i++ {
		even[i] = x[2*i]
		odd[i] = x[2*i+1]
	}

	evenFFT := fftRecursive(even)
	oddFFT := fftRecursive(odd)

	result := make([]complex128, n)
	for k := 0; k < n/2; k++ {
		t := cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n))) * oddFFT[k]
		result[k] = evenFFT[k] + t
		result[k+n/2] = evenFFT[k] - t
	}

	return result
}

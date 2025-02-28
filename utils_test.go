package conc

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestStream(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		inputs := []int{70, 5, 30, 10, 25, 55}
		var collected []string

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					output := fmt.Sprintf("output %02d", i)
					Sleep(n, time.Millisecond*time.Duration(i))
					return output, nil
				},
			)
			for output := range outputs {
				collected = append(collected, output)
			}

			return nil
		})
		if err != nil {
			t.Error(err)
		}
		if len(collected) != len(inputs) {
			t.Errorf("expected to collect an output for each input")
		}
		sortedOutputs := slices.Clone(collected)
		slices.Sort(sortedOutputs)
		if !slices.Equal(collected, sortedOutputs) {
			t.Errorf("expected output slice to be in sorted order")
		}
	})

	t.Run("Multistage", func(t *testing.T) {
		err := Block(func(n Nursery) error {
			// stage A: ints to strings
			inputs := []int{700, 50, 300, 100, 25, 550, 1000}
			outputsA := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					output := fmt.Sprintf("output %02d", i)
					Sleep(n, time.Millisecond*time.Duration(i))
					return output, nil
				},
			)

			// stage B: strings to ints
			outputsB := Stream(
				n,
				outputsA,
				func(s string) (int, error) {
					return len(s), nil
				},
			)

			for _ = range outputsB {
			}

			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})
}

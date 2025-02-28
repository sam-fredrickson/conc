package conc

import (
	"context"
	"iter"
	"sync/atomic"
	"time"
)

// Sleep is an alternative to time.Sleep that returns once d time is elapsed or
// context is done.
func Sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

type Job[T any] func(context.Context) (T, error)

// All executes all jobs in separate goroutines and stores each result in
// the returned slice.
func All[T any](jobs []Job[T], opts ...BlockOption) ([]T, error) {
	results := make([]T, len(jobs), len(jobs)) //nolint:gosimple

	err := Block(func(n Nursery) error {
		for i, job := range jobs {
			r := &results[i]
			n.Go(func() (err error) {
				*r, err = job(n)
				return err
			})
		}

		return nil
	}, opts...)

	return results, err
}

// Race executes all jobs in separate goroutines and returns first result.
// Remaining goroutines are canceled.
func Race[T any](jobs []Job[T], opts ...BlockOption) (T, error) {
	var first atomic.Bool
	var result T

	err := Block(func(n Nursery) error {
		for _, job := range jobs {
			n.Go(func() (err error) {
				r, err := job(n)
				if first.CompareAndSwap(false, true) {
					result = r
					n.(*nursery).cancel()
				}
				return err
			})
		}

		return nil
	}, opts...)

	return result, err
}

// Range iterates over a sequence and pass each value to a separate goroutine.
func Range[T any](seq iter.Seq[T], block func(context.Context, T) error, opts ...BlockOption) error {
	return Block(func(n Nursery) error {
		for v := range seq {
			value := v
			n.Go(func() error {
				return block(n, value)
			})
		}

		return nil
	}, opts...)
}

// Range2 is the same as Range except it uses a iter.Seq2 instead of iter.Seq.
func Range2[K, V any](seq iter.Seq2[K, V], block func(context.Context, K, V) error, opts ...BlockOption) error {
	return Block(func(n Nursery) error {
		for k, v := range seq {
			key := k
			value := v
			n.Go(func() error {
				return block(n, key, value)
			})
		}

		return nil
	}, opts...)
}

// Map applies f to each element of input and returns a new slice containing
// mapped results.
func Map[T any, V any](input []T, f func(context.Context, T) (V, error), opts ...BlockOption) ([]V, error) {
	results := make([]V, len(input), len(input)) //nolint:gosimple
	err := doMap(input, results, f, opts...)
	return results, err
}

// MapInPlace applies f to each element of input and returns modified input slice.
func MapInPlace[T any](input []T, f func(context.Context, T) (T, error), opts ...BlockOption) ([]T, error) {
	err := doMap(input, input, f, opts...)
	return input, err
}

func doMap[T any, V any](input []T, results []V, f func(context.Context, T) (V, error), opts ...BlockOption) error {
	return Block(func(n Nursery) error {
		for i, v := range input {
			value := v
			r := &results[i]
			n.Go(func() (err error) {
				*r, err = f(n, value)
				return err
			})
		}

		return nil
	}, opts...)
}

// Map2 applies f to each key, value pair of input and returns a new slice containing
// mapped results.
func Map2[K comparable, V any](input map[K]V, f func(context.Context, K, V) (K, V, error), opts ...BlockOption) (map[K]V, error) {
	results := make(map[K]V)
	err := doMap2(input, results, f, opts...)
	return results, err
}

// MapInPlace2 applies f to each key, value pair of input and returns modified map.
func Map2InPlace[K comparable, V any](input map[K]V, f func(context.Context, K, V) (K, V, error), opts ...BlockOption) (map[K]V, error) {
	err := doMap2(input, input, f, opts...)
	return input, err
}

func doMap2[K comparable, V any](input map[K]V, results map[K]V, f func(context.Context, K, V) (K, V, error), opts ...BlockOption) error {
	return Block(func(n Nursery) error {
		for k, v := range input {
			key := k
			value := v
			n.Go(func() error {
				newK, newV, err := f(n, key, value)
				results[newK] = newV
				return err
			})
		}

		return nil
	}, opts...)
}

// Stream concurrently transforms an input sequence into an output sequence.
//
// The order of the output sequence is non-deterministic. Items are delivered as they finish.
//
// Parameters:
//   - parent: The parent nursery that will manage the stream's lifecycle
//   - inputs: The input sequence to be transformed
//   - transform: Function that converts each input item to an output item
//   - opts: Optional configuration for the nested nursery
//
// Returns:
//   - An iterator sequence that yields transformed items as they complete
//
// Example:
//
//	_ = Block(func(n Nursery) error {
//		inputs := slices.Values([]int{1, 2, 3, 4, 5})
//		outputs := conc.Stream(n, inputs, func(_ context.Context, i int) (string, error) {
//			return fmt.Sprintf("item-%d", i), nil
//		})
//
//		for output := range outputs {
//			fmt.Println(output)
//		}
//	})
//
// Within the parent nursery, a goroutine is launched, which in turn creates a nested nursery.
// The sequence is processed entirely within this nested nursery, and the provided block
// options are passed to that nursery. This allows each stream to have separate goroutine
// limits, contexts, etc. By default, however, the nested nursery will use the parent nursery
// as its context.
//
// Error handling: If the transform function returns an error for any input item, that error
// is propagated to the parent nursery, but the stream continues processing other items until
// the parent context is canceled.
//
// This function uses an unbuffered channel for outputs, but includes context cancellation handling
// to prevent goroutines from blocking indefinitely. If the nursery's context is cancelled
// (e.g., via timeout or explicit cancellation), any blocked goroutines will detect this and
// terminate gracefully.
//
// However, if you stop consuming from the output sequence before it's fully drained (e.g.,
// breaking out of the range loop early) without cancelling the context, the transformation
// goroutines may still block. To handle this case, you can either:
//
// 1. Cancel the context when you're done consuming (recommended)
//
// 2. Drain the remaining outputs in a separate goroutine.
func Stream[I any, O any](
	parent Nursery,
	inputs iter.Seq[I],
	transform func(context.Context, I) (O, error),
	opts ...BlockOption,
) iter.Seq[O] {
	outputs := make(chan O)
	allOpts := append([]BlockOption{WithContext(parent)}, opts...)
	parent.Go(func() error {
		err := Block(
			func(stream Nursery) error {
			processing:
				for input := range inputs {
					input := input
					select {
					case <-stream.Done():
						break processing
					default:
					}
					stream.Go(func() error {
						output, err := transform(stream, input)
						if err != nil {
							return err
						}
						select {
						case outputs <- output:
						case <-stream.Done():
							return stream.Err()
						}
						return nil
					})
				}
				return nil
			},
			allOpts...,
		)
		close(outputs)
		if err != nil {
			return err
		}
		return parent.Err()
	})
	return chanSeq(outputs)
}

// chanSeq returns an iterator that yields the values of ch until it is closed.
func chanSeq[T any](ch <-chan T) iter.Seq[T] {
	return func(yield func(T) bool) {
		for v := range ch {
			if !yield(v) {
				return
			}
		}
	}
}

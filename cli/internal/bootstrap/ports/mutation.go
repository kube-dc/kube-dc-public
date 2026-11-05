package ports

import "context"

type mutationGuardKey struct{}

// WithMutationGuard binds direct subprocess helpers to the same abortable
// guard as adapter calls. Callers must check immediately before a write.
func WithMutationGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, mutationGuardKey{}, guard)
}
func BeforeMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(mutationGuardKey{}).(func(context.Context) error); ok {
		return guard(ctx)
	}
	return nil
}

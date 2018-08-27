package diodes

import gendiodes "code.cloudfoundry.org/diodes"

// OneToOneWaiter diode is optimized for a single writer and a single reader for
// byte slices.
type OneToOneWaiter struct {
	d *gendiodes.Waiter
}

// NewOneToOneWaiter initializes a new one to one diode of a given size and alerter.
// The alerter is called whenever data is dropped with an integer representing
// the number of byte slices that were dropped.
func NewOneToOneWaiter(size int, alerter gendiodes.Alerter, opts ...gendiodes.WaiterConfigOption) *OneToOneWaiter {
	return &OneToOneWaiter{
		d: gendiodes.NewWaiter(gendiodes.NewOneToOne(size, alerter), opts...),
	}
}

// Set inserts the given data into the diode.
func (d *OneToOneWaiter) Set(data []byte) {
	d.d.Set(gendiodes.GenericDataType(&data))
}

// TryNext returns the next item to be read from the diode. If the diode is
// empty it will return a nil slice of bytes and false for the bool.
func (d *OneToOneWaiter) TryNext() ([]byte, bool) {
	data, ok := d.d.TryNext()
	if !ok {
		return nil, ok
	}

	return *(*[]byte)(data), true
}

// Next will return the next item to be read from the diode. If the diode is
// empty this method will block until an item is available to be read.
func (d *OneToOneWaiter) Next() []byte {
	data := d.d.Next()
	if data == nil {
		return nil
	}

	return *(*[]byte)(data)
}

package handler

import "testing"

func TestValidateCreateReservationRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateReservationRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  5,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid warehouse",
			req: CreateReservationRequest{
				WarehouseID: 0,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  5,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "empty items",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items:       nil,
			},
			wantErr: true,
		},
		{
			name: "invalid product",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 0,
						Quantity:  5,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid quantity",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  0,
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateReservationRequest(tt.req)

			if (err != nil) != tt.wantErr {
				t.Errorf(
					"validateCreateReservationRequest() error = %v, wantErr = %v",
					err,
					tt.wantErr,
				)
			}
		})
	}
}

package subscription

import "context"

// DevBillingGateway runs subscription flows offline.
type DevBillingGateway struct{}

func (DevBillingGateway) EnsureCustomer(_ context.Context, existingID, userPublicID string) (string, error) {
	if existingID != "" {
		return existingID, nil
	}
	return "cus_dev_" + userPublicID, nil
}

func (g DevBillingGateway) CreateSubscriptionCheckout(_ context.Context, _, userPublicID, _, successURL, _ string) (string, string, error) {
	return successURL + "?session_id=dev_sub", "dev_sub", nil
}

func (DevBillingGateway) CreatePortalSession(_ context.Context, _, returnURL string) (string, error) {
	return returnURL + "?portal=dev", nil
}

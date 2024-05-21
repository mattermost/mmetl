// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package model

type BootstrapSelfHostedSignupRequest struct {
	Email string `json:"email"`
	Reset bool   `json:"reset"`
}

type SubscribeNewsletterRequest struct {
	Email             string `json:"email"`
	ServerID          string `json:"server_id"`
	SubscribedContent string `json:"subscribed_content"`
}

type BootstrapSelfHostedSignupResponse struct {
	Progress string `json:"progress"`
	// email listed on the JWT claim
	Email string `json:"email"`
}

type BootstrapSelfHostedSignupResponseInternal struct {
	Progress string `json:"progress"`
	License  string `json:"license"`
}

// email contained in token, so not in the request body.
type SelfHostedCustomerForm struct {
	FirstName       string   `json:"first_name"`
	LastName        string   `json:"last_name"`
	BillingAddress  *Address `json:"billing_address"`
	ShippingAddress *Address `json:"shipping_address"`
	Organization    string   `json:"organization"`
}

type SelfHostedConfirmPaymentMethodRequest struct {
	StripeSetupIntentID string                      `json:"stripe_setup_intent_id"`
	Subscription        *CreateSubscriptionRequest  `json:"subscription"`
	ExpandRequest       *SelfHostedExpansionRequest `json:"expand_request"`
}

// SelfHostedSignupPaymentResponse contains feels needed for self hosted signup to confirm payment and receive license.
type SelfHostedSignupCustomerResponse struct {
	CustomerId        string `json:"customer_id"`
	SetupIntentId     string `json:"setup_intent_id"`
	SetupIntentSecret string `json:"setup_intent_secret"`
	Progress          string `json:"progress"`
}

// SelfHostedSignupConfirmResponse contains data received on successful self hosted signup
type SelfHostedSignupConfirmResponse struct {
	License  string `json:"license"`
	Progress string `json:"progress"`
}

type SelfHostedSignupConfirmClientResponse struct {
	License  map[string]string `json:"license"`
	Progress string            `json:"progress"`
}

type SelfHostedBillingAccessRequest struct {
	LicenseId string `json:"license_id"`
}

type SelfHostedBillingAccessResponse struct {
	Token string `json:"token"`
}

type SelfHostedExpansionRequest struct {
	Seats     int    `json:"seats"`
	LicenseId string `json:"license_id"`
}

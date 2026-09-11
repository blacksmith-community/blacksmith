package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
	internalhttp "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Test static errors.
var (
	ErrTestSomeError = errors.New("some error")
)

// NewTestClient creates a new test client with the given base URL.
func NewTestClient(baseURL string) *Client {
	// Create HTTP client without token manager for testing
	httpClient := internalhttp.NewClient(baseURL, nil)

	client := &Client{
		httpClient: httpClient,
		baseURL:    baseURL,
	}

	// Initialize resource clients
	client.initializeResourceClients()

	return client
}

// TestCreateOperation represents a generic create operation test case.
type TestCreateOperation[TRequest, TResponse any] struct {
	Name         string
	Request      *TRequest
	ExpectedPath string
	StatusCode   int
	Response     any // Can be *TResponse or error response map
	WantErr      bool
	ErrMessage   string
}

// TestGetOperation represents a generic get operation test case.
type TestGetOperation[TResponse any] struct {
	Name         string
	GUID         string
	ExpectedPath string
	StatusCode   int
	Response     *TResponse
	WantErr      bool
	ErrMessage   string
}

// TestUpdateOperation represents a generic update operation test case.
type TestUpdateOperation[TRequest, TResponse any] struct {
	Name         string
	GUID         string
	Request      *TRequest
	ExpectedPath string
	StatusCode   int
	Response     *TResponse
	WantErr      bool
	ErrMessage   string
}

// TestDeleteOperation represents a generic delete operation test case.
type TestDeleteOperation struct {
	Name         string
	GUID         string
	ExpectedPath string
	StatusCode   int
	WantErr      bool
	ErrMessage   string
	Response     any
}

// RunCreateTests runs a series of create operation tests.
func RunCreateTests[TRequest, TResponse any](
	t *testing.T,
	tests []TestCreateOperation[TRequest, TResponse],
	createFunc func(*Client) func(context.Context, *TRequest) (*TResponse, error),
	requestDecoder func(*http.Request) (*TRequest, error),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "POST", request.Method)

				if requestDecoder != nil {
					_, err := requestDecoder(request)
					assert.NoError(t, err)
				}

				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.StatusCode)

				if testCase.Response != nil {
					_ = json.NewEncoder(writer).Encode(testCase.Response)
				}
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			createFn := createFunc(client)
			result, err := createFn(context.Background(), testCase.Request)

			if testCase.WantErr {
				require.Error(t, err)

				if testCase.ErrMessage != "" {
					assert.Contains(t, err.Error(), testCase.ErrMessage)
				}

				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// RunGetTests runs a series of get operation tests.
func RunGetTests[TResponse any](
	t *testing.T,
	tests []TestGetOperation[TResponse],
	getFunc func(*Client) func(context.Context, string) (*TResponse, error),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "GET", request.Method)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.StatusCode)

				if testCase.WantErr {
					// Return error response format
					errorResponse := map[string]any{
						"errors": []map[string]any{
							{
								"code":   constants.CFErrorCodeNotFound,
								"title":  "CF-ResourceNotFound",
								"detail": "Resource not found",
							},
						},
					}
					_ = json.NewEncoder(writer).Encode(errorResponse)
				} else if testCase.Response != nil {
					_ = json.NewEncoder(writer).Encode(testCase.Response)
				}
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			getFn := getFunc(client)
			result, err := getFn(context.Background(), testCase.GUID)

			if testCase.WantErr {
				require.Error(t, err)

				if testCase.ErrMessage != "" {
					assert.Contains(t, err.Error(), testCase.ErrMessage)
				}

				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// RunUpdateTests runs a series of update operation tests.
func RunUpdateTests[TRequest, TResponse any](
	t *testing.T,
	tests []TestUpdateOperation[TRequest, TResponse],
	updateFunc func(string, context.Context, string, *TRequest) (*TResponse, error),
	requestDecoder func(*http.Request) (*TRequest, error),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "PATCH", request.Method)

				if requestDecoder != nil {
					_, err := requestDecoder(request)
					assert.NoError(t, err)
				}

				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.StatusCode)

				if testCase.Response != nil {
					_ = json.NewEncoder(writer).Encode(testCase.Response)
				}
			}))
			defer server.Close()

			result, err := updateFunc(server.URL, context.Background(), testCase.GUID, testCase.Request)

			if testCase.WantErr {
				require.Error(t, err)

				if testCase.ErrMessage != "" {
					assert.Contains(t, err.Error(), testCase.ErrMessage)
				}

				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// RunDeleteTests runs a series of delete operation tests.
func RunDeleteTests(
	t *testing.T,
	tests []TestDeleteOperation,
	deleteFunc func(string, context.Context, string) error,
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "DELETE", request.Method)

				if testCase.Response != nil {
					writer.Header().Set("Content-Type", "application/json")
				}

				writer.WriteHeader(testCase.StatusCode)

				if testCase.Response != nil {
					_ = json.NewEncoder(writer).Encode(testCase.Response)
				}
			}))
			defer server.Close()

			err := deleteFunc(server.URL, context.Background(), testCase.GUID)

			if testCase.WantErr {
				require.Error(t, err)

				if testCase.ErrMessage != "" {
					assert.Contains(t, err.Error(), testCase.ErrMessage)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// RunDeleteJobTests runs a series of delete operation tests that return jobs.
func RunDeleteJobTests(
	t *testing.T,
	tests []TestDeleteOperation,
	deleteFunc func(context.Context, string) (*capi.Job, error),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "DELETE", request.Method)

				if testCase.Response != nil {
					writer.Header().Set("Content-Type", "application/json")
				}

				writer.WriteHeader(testCase.StatusCode)

				if testCase.Response != nil {
					_ = json.NewEncoder(writer).Encode(testCase.Response)
				}
			}))
			defer server.Close()

			_, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			job, err := deleteFunc(context.Background(), testCase.GUID)

			if testCase.WantErr {
				require.Error(t, err)

				if testCase.ErrMessage != "" {
					assert.Contains(t, err.Error(), testCase.ErrMessage)
				}

				assert.Nil(t, job)
			} else {
				require.NoError(t, err)
				require.NotNil(t, job)
			}
		})
	}
}

// TestAppActionOperation represents a test case for app action operations.
//
// start/stop/restart/restage all POST to /v3/apps/{guid}/actions/{action}
// and return a job (async per CF v3 API). ExpectedState is retained for
// readability in test names; the server responds with 202 + Location
// header pointing at a fixed test job GUID, and the client extracts it.
type TestAppActionOperation struct {
	Name          string
	Action        string
	ExpectedState string
	ActionFunc    func(*Client) func(context.Context, string) (*capi.Job, error)
}

// RunAppActionTests runs a series of app action tests.
func RunAppActionTests(t *testing.T, tests []TestAppActionOperation) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/v3/apps/app-guid/actions/"+testCase.Action, request.URL.Path)
				assert.Equal(t, "POST", request.Method)
				writer.Header().Set("Location", "/v3/jobs/test-job-guid")
				writer.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			actionFunc := testCase.ActionFunc(client)
			job, err := actionFunc(context.Background(), "app-guid")
			require.NoError(t, err)
			require.NotNil(t, job)
			assert.Equal(t, "test-job-guid", job.GUID)
		})
	}
}

// RunErrorTypeTests runs a series of error type checking tests with a common pattern.
func RunErrorTypeTests(t *testing.T, testName string, targetErrorCode int, checkFunction func(error) bool) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "APIError with target code",
				err:      &capi.APIError{Code: targetErrorCode},
				expected: true,
			},
			{
				name:     "APIError other error",
				err:      &capi.APIError{Code: capi.ErrorCodeNotFound},
				expected: targetErrorCode == capi.ErrorCodeNotFound,
			},
			{
				name: "ResponseError with target code",
				err: &capi.ResponseError{
					Errors: []capi.APIError{
						{Code: targetErrorCode},
					},
				},
				expected: true,
			},
			{
				name: "ResponseError without target code",
				err: &capi.ResponseError{
					Errors: []capi.APIError{
						{Code: capi.ErrorCodeNotFound},
					},
				},
				expected: targetErrorCode == capi.ErrorCodeNotFound,
			},
			{
				name:     "other error type",
				err:      ErrTestSomeError,
				expected: false,
			},
		}

		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				assert.Equal(t, testCase.expected, checkFunction(testCase.err))
			})
		}
	})
}

// TestRelationshipOperation represents a test case for relationship operations.
type TestRelationshipOperation struct {
	Name             string
	ResourceGUID     string
	TargetGUID       string
	ExpectedPath     string
	RelationshipFunc func(*Client) func(context.Context, string, string) (*capi.Relationship, error)
}

// RunRelationshipTests runs a series of relationship operation tests.
func RunRelationshipTests(t *testing.T, tests []TestRelationshipOperation) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.ExpectedPath, request.URL.Path)
				assert.Equal(t, "PATCH", request.Method)

				var req capi.Relationship

				_ = json.NewDecoder(request.Body).Decode(&req)
				assert.Equal(t, testCase.TargetGUID, req.Data.GUID)

				relationship := capi.Relationship{
					Data: req.Data,
				}

				_ = json.NewEncoder(writer).Encode(relationship)
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			relationshipFunc := testCase.RelationshipFunc(client)
			relationship, err := relationshipFunc(context.Background(), testCase.ResourceGUID, testCase.TargetGUID)
			require.NoError(t, err)
			assert.NotNil(t, relationship.Data)
			assert.Equal(t, testCase.TargetGUID, relationship.Data.GUID)
		})
	}
}

// RunDownloadTest runs a test for a download operation.
func RunDownloadTest(
	t *testing.T,
	resourceType string,
	resourceGUID string,
	expectedPath string,
	expectedContent []byte,
	downloadFunc func(*Client) func(context.Context, string) ([]byte, error),
) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, expectedPath, request.URL.Path)
		assert.Equal(t, "GET", request.Method)

		writer.Header().Set("Content-Type", "application/zip")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(expectedContent)
	}))
	defer server.Close()

	client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
	require.NoError(t, err)

	downloadFn := downloadFunc(client)
	content, err := downloadFn(context.Background(), resourceGUID)
	require.NoError(t, err)
	assert.Equal(t, expectedContent, content)
}

// StringPtr is a helper function that returns a pointer to the given string.
func StringPtr(s string) *string {
	return &s
}

// RunBasicUpdateTests runs the most common pattern of update tests using RunUpdateTests.
func RunBasicUpdateTests[TRequest, TResponse any](
	t *testing.T,
	resourceType string,
	resourceGUID string,
	resourcePath string,
	updateRequest *TRequest,
	response *TResponse,
	updateFunc func(*Client) func(context.Context, string, *TRequest) (*TResponse, error),
	requestDecoder func(*http.Request) (*TRequest, error),
) {
	t.Helper()

	tests := []TestUpdateOperation[TRequest, TResponse]{
		{
			Name:         "successful update",
			GUID:         resourceGUID,
			ExpectedPath: resourcePath,
			StatusCode:   http.StatusOK,
			Request:      updateRequest,
			Response:     response,
			WantErr:      false,
		},
	}

	RunUpdateTests(t, tests, func(serverURL string, ctx context.Context, guid string, request *TRequest) (*TResponse, error) {
		client, err := New(ctx, &capi.Config{APIEndpoint: serverURL})
		if err != nil {
			return nil, err
		}

		updateFn := updateFunc(client)

		return updateFn(ctx, guid, request)
	}, requestDecoder)
}

// RunStandardUpdateTest runs a standardized update test with common metadata patterns.
func RunStandardUpdateTest[TRequest, TResponse any](
	t *testing.T,
	resourceType string,
	resourceGUID string,
	resourcePath string,
	updateRequest *TRequest,
	response *TResponse,
	updateFunc func(*Client) func(context.Context, string, *TRequest) (*TResponse, error),
) {
	t.Helper()

	RunBasicUpdateTests(t, resourceType, resourceGUID, resourcePath, updateRequest, response,
		updateFunc,
		func(request *http.Request) (*TRequest, error) {
			var requestBody TRequest

			err := json.NewDecoder(request.Body).Decode(&requestBody)
			if err != nil {
				return &requestBody, fmt.Errorf("failed to decode request body: %w", err)
			}

			return &requestBody, nil
		})
}

// NameUpdateTestCase represents a test case for name-only updates.
type NameUpdateTestCase[TRequest, TResponse any] struct {
	ResourceType    string
	ResourceGUID    string
	ResourcePath    string
	OriginalName    string
	NewName         string
	CreateRequest   func(string) *TRequest
	CreateResponse  func(string, string) *TResponse
	ExtractName     func(*TRequest) string
	ExtractNameResp func(*TResponse) string
	UpdateFunc      func(*Client) func(context.Context, string, *TRequest) (*TResponse, error)
}

// RunNameUpdateTest runs a standardized name-only update test.
func RunNameUpdateTest[TRequest, TResponse any](t *testing.T, testCase NameUpdateTestCase[TRequest, TResponse]) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, testCase.ResourcePath, request.URL.Path)
		assert.Equal(t, "PATCH", request.Method)

		var req TRequest

		_ = json.NewDecoder(request.Body).Decode(&req)
		assert.Equal(t, testCase.NewName, testCase.ExtractName(&req))

		response := testCase.CreateResponse(testCase.ResourceGUID, testCase.NewName)
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
	require.NoError(t, err)

	updateFn := testCase.UpdateFunc(client)
	request := testCase.CreateRequest(testCase.NewName)
	result, err := updateFn(context.Background(), testCase.ResourceGUID, request)

	require.NoError(t, err)
	assert.Equal(t, testCase.NewName, testCase.ExtractNameResp(result))
}

// RunCreateTestsSimple runs a simple create test pattern that matches the legacy helpers.
func RunCreateTestsSimple[TRequest, TResponse any](
	t *testing.T,
	tests []struct {
		name         string
		request      *TRequest
		response     any
		statusCode   int
		expectedPath string
		wantErr      bool
		errMessage   string
	},
	createFunc func(*Client) func(context.Context, *TRequest) (*TResponse, error),
	validateResponse func(*TResponse),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.expectedPath, request.URL.Path)
				assert.Equal(t, "POST", request.Method)

				var requestBody TRequest

				err := json.NewDecoder(request.Body).Decode(&requestBody)
				assert.NoError(t, err)

				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.statusCode)
				_ = json.NewEncoder(writer).Encode(testCase.response)
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			createFn := createFunc(client)
			result, err := createFn(context.Background(), testCase.request)

			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), testCase.errMessage)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)

				if validateResponse != nil {
					validateResponse(result)
				}
			}
		})
	}
}

// RunGetTestsSimple runs a simple get test pattern that matches the legacy helpers.
func RunGetTestsSimple[TResponse any](
	t *testing.T,
	tests []struct {
		name         string
		guid         string
		response     any
		statusCode   int
		expectedPath string
		wantErr      bool
		errMessage   string
	},
	getFunc func(*Client) func(context.Context, string) (*TResponse, error),
	validateResponse func(string, *TResponse),
) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, testCase.expectedPath, request.URL.Path)
				assert.Equal(t, "GET", request.Method)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.statusCode)
				_ = json.NewEncoder(writer).Encode(testCase.response)
			}))
			defer server.Close()

			client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
			require.NoError(t, err)

			getFn := getFunc(client)
			result, err := getFn(context.Background(), testCase.guid)

			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), testCase.errMessage)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)

				if validateResponse != nil {
					validateResponse(testCase.guid, result)
				}
			}
		})
	}
}

// RunShareWithTest runs a generic share-with relationship test.
func RunShareWithTest(
	t *testing.T,
	testName string,
	resourceType string,
	resourceGUID string,
	relationshipPath string,
	inputGUIDs []string,
	responseGUIDs []string,
	shareFunc func(*Client) func(context.Context, string, []string) (*capi.ToManyRelationship, error),
) {
	t.Helper()

	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, relationshipPath, request.URL.Path)
			assert.Equal(t, "POST", request.Method)

			var requestBody struct {
				Data []capi.RelationshipData `json:"data"`
			}

			err := json.NewDecoder(request.Body).Decode(&requestBody)
			assert.NoError(t, err)
			assert.Len(t, requestBody.Data, len(inputGUIDs))

			response := capi.ToManyRelationship{
				Data: make([]capi.RelationshipData, len(responseGUIDs)),
			}
			for i, guid := range responseGUIDs {
				response.Data[i] = capi.RelationshipData{GUID: guid}
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		shareFn := shareFunc(client)
		relationship, err := shareFn(context.Background(), resourceGUID, inputGUIDs)
		require.NoError(t, err)
		require.NotNil(t, relationship)
		assert.Len(t, relationship.Data, len(responseGUIDs))
	})
}

// RunListTestSimple runs a generic list test with custom response data.
func RunListTestSimple[TResource any](
	t *testing.T,
	testName string,
	expectedPath string,
	responseData []TResource,
	listFunc func(*Client) func(context.Context, string, any) (*capi.ListResponse[TResource], error),
	validateResources func([]TResource),
	resourceGUID string,
) {
	t.Helper()

	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "GET", request.Method)

			response := capi.ListResponse[TResource]{
				Pagination: capi.Pagination{
					TotalResults: len(responseData),
					TotalPages:   1,
				},
				Resources: responseData,
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		listFn := listFunc(client)
		result, err := listFn(context.Background(), resourceGUID, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, len(responseData), result.Pagination.TotalResults)

		if validateResources != nil {
			validateResources(result.Resources)
		}
	})
}

// RunQuotaCreateTest runs a generic quota create test.
func RunQuotaCreateTest[TRequest, TResponse any](
	t *testing.T,
	testName string,
	expectedPath string,
	expectedName string,
	totalMemory int,
	createRequestFactory func(string) *TRequest,
	createResponseFactory func(string, string, int) *TResponse,
	createFunc func(*Client) func(context.Context, *TRequest) (*TResponse, error),
	validateResponse func(*TResponse),
) {
	t.Helper()

	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "POST", request.Method)

			var req TRequest

			_ = json.NewDecoder(request.Body).Decode(&req)

			response := createResponseFactory("quota-guid", expectedName, totalMemory)

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		createFn := createFunc(client)
		request := createRequestFactory(expectedName)
		result, err := createFn(context.Background(), request)
		require.NoError(t, err)

		if validateResponse != nil {
			validateResponse(result)
		}
	})
}

// RunRoleCreateTest runs a generic role create test.
func RunRoleCreateTest(
	t *testing.T,
	testName string,
	roleType string,
	userGUID string,
	orgGUID string,
	spaceGUID string,
	expectedRoleType string,
	expectedRelationships capi.RoleRelationships,
) {
	t.Helper()

	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, "/v3/roles", request.URL.Path)
			assert.Equal(t, "POST", request.Method)

			var requestBody capi.RoleCreateRequest

			err := json.NewDecoder(request.Body).Decode(&requestBody)
			assert.NoError(t, err)

			assert.Equal(t, expectedRoleType, requestBody.Type)
			assert.Equal(t, userGUID, requestBody.Relationships.User.Data.GUID)

			if orgGUID != "" {
				assert.Equal(t, orgGUID, requestBody.Relationships.Organization.Data.GUID)
			}

			if spaceGUID != "" {
				assert.Equal(t, spaceGUID, requestBody.Relationships.Space.Data.GUID)
			}

			now := time.Now()
			role := capi.Role{
				Resource: capi.Resource{
					GUID:      "role-guid",
					CreatedAt: now,
					UpdatedAt: now,
				},
				Type:          requestBody.Type,
				Relationships: requestBody.Relationships,
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(role)
		}))
		defer server.Close()

		httpClient := internalhttp.NewClient(server.URL, nil)
		roles := NewRolesClient(httpClient)

		request := &capi.RoleCreateRequest{
			Type:          roleType,
			Relationships: expectedRelationships,
		}

		role, err := roles.Create(context.Background(), request)
		require.NoError(t, err)
		assert.NotNil(t, role)
		assert.Equal(t, "role-guid", role.GUID)
		assert.Equal(t, expectedRoleType, role.Type)
	})
}

// RunServiceListTest runs a generic service list test with custom validation.
func RunServiceListTest[TResource any](
	t *testing.T,
	testName string,
	expectedPath string,
	queryValidation func(*http.Request),
	responseData []TResource,
	clientFactory func(*internalhttp.Client) any,
	listCall func(any) (*capi.ListResponse[TResource], error),
	validateResults func([]TResource),
) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "GET", request.Method)

			if queryValidation != nil {
				queryValidation(request)
			}

			response := capi.ListResponse[TResource]{
				Pagination: capi.Pagination{
					TotalResults: len(responseData),
					TotalPages:   1,
					First:        capi.Link{Href: expectedPath + "?page=1"},
					Last:         capi.Link{Href: expectedPath + "?page=1"},
				},
				Resources: responseData,
			}

			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		httpClient := internalhttp.NewClient(server.URL, nil)
		client := clientFactory(httpClient)

		list, err := listCall(client)
		require.NoError(t, err)
		assert.NotNil(t, list)
		assert.Equal(t, len(responseData), list.Pagination.TotalResults)
		assert.Len(t, list.Resources, len(responseData))

		if validateResults != nil {
			validateResults(list.Resources)
		}
	})
}

// RunJobDeleteTest runs a generic delete test that returns a job.
//
// CF V3 async-delete contract: 202 Accepted with empty body, async job
// reference in the Location header. The `operationType` parameter is kept
// for callsite-clarity (so the test name documents which CF op is under
// test) but no longer asserted on the returned Job (Operation/State live
// in the /v3/jobs/{guid} response body, not in the Location-derived stub).
func RunJobDeleteTest(
	t *testing.T,
	testName string,
	expectedPath string,
	operationType string,
	clientFactory func(*internalhttp.Client) any,
	deleteCall func(any) (*capi.Job, error),
) {
	t.Helper()

	_ = operationType

	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "DELETE", request.Method)

			writer.Header().Set("Location", "/v3/jobs/job-guid")
			writer.WriteHeader(http.StatusAccepted)
		}))
		defer server.Close()

		httpClient := internalhttp.NewClient(server.URL, nil)
		client := clientFactory(httpClient)

		job, err := deleteCall(client)
		require.NoError(t, err)
		require.NotNil(t, job)
		assert.Equal(t, "job-guid", job.GUID)
	})
}

// RunSpaceUserListTest runs a generic space user list test.
func RunSpaceUserListTest(
	t *testing.T,
	testName string,
	expectedPath string,
	userGUID string,
	userName string,
	listFunc func(*Client) func(context.Context, string, *capi.QueryParams) (*capi.ListResponse[capi.User], error),
) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "GET", request.Method)

			response := capi.ListResponse[capi.User]{
				Resources: []capi.User{
					{
						Resource: capi.Resource{GUID: userGUID},
						Username: userName,
					},
				},
			}

			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		listFn := listFunc(client)
		result, err := listFn(context.Background(), "space-guid", nil)
		require.NoError(t, err)
		assert.Len(t, result.Resources, 1)
		assert.Equal(t, userName, result.Resources[0].Username)
	})
}

// RunSimpleListTest runs a simple list test with query parameters and expected resource count.
func RunSimpleListTest[TResource any](
	t *testing.T,
	testName string,
	expectedPath string,
	resourceCount int,
	createResource func(int) TResource,
	listFunc func(*Client) func(context.Context, string, *capi.QueryParams) (*capi.ListResponse[TResource], error),
	resourceGUID string,
	validateResources func([]TResource),
) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "GET", request.Method)

			resources := make([]TResource, resourceCount)
			for i := range resourceCount {
				resources[i] = createResource(i)
			}

			response := capi.ListResponse[TResource]{
				Pagination: capi.Pagination{
					TotalResults: resourceCount,
					TotalPages:   1,
				},
				Resources: resources,
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		listFn := listFunc(client)
		result, err := listFn(context.Background(), resourceGUID, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, resourceCount, result.Pagination.TotalResults)

		if validateResources != nil {
			validateResources(result.Resources)
		}
	})
}

// uploadMultipartFile handles common multipart file upload logic.
func uploadMultipartFile(ctx context.Context, httpClient *internalhttp.Client, path string, filename string, bits []byte, resourceType string) ([]byte, error) {
	// Create multipart form data
	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)

	// Add the file field
	part, err := writer.CreateFormFile("bits", filename)
	if err != nil {
		return nil, fmt.Errorf("creating form file: %w", err)
	}

	_, err = part.Write(bits)
	if err != nil {
		return nil, fmt.Errorf("writing file to form: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	// Use PostRaw to send multipart form data
	resp, err := httpClient.PostRaw(ctx, path, buf.Bytes(), writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", resourceType, err)
	}

	return resp.Body, nil
}

// RunCreateTestWithValidation runs a create test with validation.
func RunCreateTestWithValidation(t *testing.T, testName, expectedPath string, statusCode int, response any, wantErr bool, errMessage string, testFunc func(*Client) error) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "POST", request.Method)

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(statusCode)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		err = testFunc(client)

		if wantErr {
			require.Error(t, err)
			assert.Contains(t, err.Error(), errMessage)
		} else {
			require.NoError(t, err)
		}
	})
}

// RunGetTestWithValidation runs a get test with validation.
func RunGetTestWithValidation(t *testing.T, testName, guid, expectedPath string, statusCode int, response any, wantErr bool, errMessage string, testFunc func(*Client, string) error) {
	t.Helper()
	t.Run(testName, func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			assert.Equal(t, expectedPath, request.URL.Path)
			assert.Equal(t, "GET", request.Method)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(statusCode)
			_ = json.NewEncoder(writer).Encode(response)
		}))
		defer server.Close()

		client, err := New(context.Background(), &capi.Config{APIEndpoint: server.URL})
		require.NoError(t, err)

		err = testFunc(client, guid)

		if wantErr {
			require.Error(t, err)
			assert.Contains(t, err.Error(), errMessage)
		} else {
			require.NoError(t, err)
		}
	})
}

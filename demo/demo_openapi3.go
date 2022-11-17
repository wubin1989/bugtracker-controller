package service

import "github.com/unionj-cloud/go-doudou/v2/framework/rest"

func init() {
	rest.Oas = `{"openapi":"3.0.2","info":{"title":"Demo","version":"v20221117"},"servers":[{"url":"http://localhost:6060"}],"paths":{"/health":{"get":{"responses":{"200":{"description":"","content":{"application/json":{"schema":{"$ref":"#/components/schemas/GetHealthResp"}}}}}}}},"components":{"schemas":{"GetHealthResp":{"title":"GetHealthResp","type":"object","properties":{"status":{"type":"string"}},"required":["status"]}}}}`
}

package bosh

import "context"

// ListVariables returns the variables the deployment uses. Each carries the
// CredHub version ID the director deployed it with.
func (d *DirectorAdapter) ListVariables(ctx context.Context, deployment string) (_ []Variable, err error) {
	defer d.explainContext(ctx, "ListVariables", &err)

	d.log.Debugf("Calling GET /deployments/%s/variables", deployment)

	listed, err := d.api.Variables().List(ctx, deployment)
	if err != nil {
		d.log.Errorf("Failed to list variables of deployment %s: %v", deployment, err)

		return nil, d.requestFailure("list variables of deployment "+deployment, err)
	}

	variables := make([]Variable, 0, len(listed))
	for _, variable := range listed {
		variables = append(variables, Variable{ID: variable.ID, Name: variable.Name})
	}

	return variables, nil
}

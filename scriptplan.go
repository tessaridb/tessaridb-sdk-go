package tessaridb

import "fmt"

// The plan of a records outcome, and the exactness that rides inside it.
//
// On the wire exactness is its own tail field with three states; here it is two
// plan keys, and the three states survive as: `exact` true, `exact` false with
// `inexact` carrying the reason, and NO `exact` key at all — a node that predates
// the field. A client MUST NOT read the third as the first. It is the same
// distinction the wire draws, carried by a key rather than by a byte.

func scriptPlan(j any) (Plan, error) {
	object, ok := j.(map[string]any)
	if !ok {
		return Plan{}, fmt.Errorf("tessaridb: a records outcome carries a plan object")
	}

	plan := Plan{}
	for _, field := range []struct {
		key  string
		into *string
	}{
		{"access", &plan.Access},
		{"source", &plan.Source},
		{"table", &plan.Table},
		{"index", &plan.Index},
		{"shape", &plan.Shape},
	} {
		if raw, present := object[field.key]; present {
			word, err := jsonText(raw, "a plan's "+field.key)
			if err != nil {
				return Plan{}, err
			}
			*field.into = word
		}
	}
	if plan.Access == "" {
		return Plan{}, fmt.Errorf("tessaridb: a plan always names its access")
	}

	for _, field := range []struct {
		key  string
		into **int64
	}{
		{"columns", &plan.Columns},
		{"cells", &plan.Cells},
		{"at_most", &plan.AtMost},
	} {
		raw, present := object[field.key]
		if !present {
			continue
		}
		n, err := jsonNumber(raw, "a plan's "+field.key)
		if err != nil {
			return Plan{}, err
		}
		count, err := n.Int64()
		if err != nil {
			return Plan{}, fmt.Errorf("tessaridb: %q is not a count", n)
		}
		*field.into = &count
	}

	exact, present := object["exact"]
	if !present {
		// Unstated, and deliberately not Exact. A node that predates the field
		// did not serve exact answers and forget to say so — it made no claim.
		return plan, nil
	}
	claimed, ok := exact.(bool)
	if !ok {
		return Plan{}, fmt.Errorf("tessaridb: a plan's exact is a boolean")
	}
	if claimed {
		plan.Exact = Exactness{State: Exact}
		return plan, nil
	}
	reason, err := jsonText(object["inexact"], "a plan's inexact reason")
	if err != nil {
		return Plan{}, err
	}
	plan.Exact = Exactness{State: Inexact, Reason: reason}
	return plan, nil
}

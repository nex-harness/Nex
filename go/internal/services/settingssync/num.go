package settingssync

import "strconv"

func fmtSscanImpl(s string, out any) (int, error) {
	switch t := out.(type) {
	case *float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		*t = f
		return len(s), nil
	case *int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		*t = n
		return len(s), nil
	}
	return 0, strconv.ErrSyntax
}

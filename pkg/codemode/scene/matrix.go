package scene

import "math"

// mat4 is a row-major affine transform.
type mat4 [4][4]float64

func identity() mat4 {
	return mat4{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}
}

func (a mat4) mul(b mat4) mat4 {
	var r mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			for k := 0; k < 4; k++ {
				r[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return r
}

func (a mat4) apply(p [3]float64) [3]float64 {
	var r [3]float64
	for i := 0; i < 3; i++ {
		r[i] = a[i][0]*p[0] + a[i][1]*p[1] + a[i][2]*p[2] + a[i][3]
	}
	return r
}

// localMatrix is T * R * S with R = Rx * Ry * Rz, which is three.js's
// Object3D composition for Euler order "XYZ".
func localMatrix(n *Node) mat4 {
	m := identity()
	m[0][3], m[1][3], m[2][3] = n.Position[0], n.Position[1], n.Position[2]
	if n.Rotation != nil {
		rad := math.Pi / 180
		m = m.mul(rotX(n.Rotation[0] * rad)).mul(rotY(n.Rotation[1] * rad)).mul(rotZ(n.Rotation[2] * rad))
	}
	if n.Scale != nil {
		s := identity()
		s[0][0], s[1][1], s[2][2] = n.Scale[0], n.Scale[1], n.Scale[2]
		m = m.mul(s)
	}
	return m
}

func rotX(a float64) mat4 {
	c, s := math.Cos(a), math.Sin(a)
	return mat4{{1, 0, 0, 0}, {0, c, -s, 0}, {0, s, c, 0}, {0, 0, 0, 1}}
}

func rotY(a float64) mat4 {
	c, s := math.Cos(a), math.Sin(a)
	return mat4{{c, 0, s, 0}, {0, 1, 0, 0}, {-s, 0, c, 0}, {0, 0, 0, 1}}
}

func rotZ(a float64) mat4 {
	c, s := math.Cos(a), math.Sin(a)
	return mat4{{c, -s, 0, 0}, {s, c, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}
}

# Maintainer: Vincent Petithory <vincent.petithory at gmail dot com>

pkgname=i3cat-git
pkgver=r3.417f5ba
pkgrel=1
pkgdesc='A simple program to combine multiple i3bar JSON inputs into one to forward to i3bar.'
arch=('x86_64' 'i686' 'ARM')
url='https://github.com/vincent-petithory/i3cat'
license=('MIT')
makedepends=('go' 'git')
depends=()
optdepends=('i3-wm: for i3bar')
options=('!strip' '!emptydirs')
source='git+https://github.com/vincent-petithory/i3cat.git'
md5sums=('SKIP')
_gourl='github.com/vincent-petithory/i3cat'

pkgver() {
  cd "$srcdir/i3cat"
  printf "r%s.%s" "$(git rev-list --count HEAD)" "$(git rev-parse --short HEAD)"
}

build() {
  p="$(dirname "${srcdir}/src/${_gourl}")"
  mkdir -p "$p"
  mv "$srcdir/i3cat" "$p"
  export GOPATH="$srcdir"
  go get -v "${_gourl}/..."
  go install "${_gourl}/..."
}

package() {
  install -D -m755 "${srcdir}/bin/i3cat" "${pkgdir}/usr/bin/i3cat"
  install -D -m644 "${srcdir}/src/${_gourl}/LICENSE" "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"
}

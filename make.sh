#!/bin/bash

PRISMJS_VERSION="76dde18a575831c91491895193f56081ac08b0c5" # v1.30.0
FAENZA_ICONS_URL="http://slackware.uk/sbosrcarch/by-md5/e/9/e9bd6106d13017ce06d24b586259ae9c/faenza-icon-theme_1.3.zip"

mkdir tmp
cd tmp

## Default theme related

THEME_STATIC_PATH=../themes/DarkAndDark/static

# prism.js (Syntax Highlighter)
mkdir ${THEME_STATIC_PATH}/prismjs
git clone https://github.com/LeaVerou/prism.git
cd prism
git checkout ${PRISMJS_VERSION}
cat themes/prism-okaidia.min.css plugins/{line-numbers/prism-line-numbers.min.css,line-highlight/prism-line-highlight.min.css} > ../${THEME_STATIC_PATH}/prismjs/prism.css
cat components/prism-{core,clike,markup,javascript,bash,c,coffeescript,cpp,csharp,css,css-extras,go,haskell,ini,java,latex,markup-templating,objectivec,php,php-extras,python,ruby,scss,sql,swift,twig}.min.js plugins/{line-numbers/prism-line-numbers.min.js,line-highlight/prism-line-highlight.min.js} > ../${THEME_STATIC_PATH}/prismjs/prism.js
cd ..

# Faenza icons
mkdir ${THEME_STATIC_PATH}/faenzaicons
wget ${FAENZA_ICONS_URL} -O faenza_icons.zip
md5sum faenza_icons.zip | grep 'e9bd6106d13017ce06d24b586259ae9c' || exit 1
mkdir faenza_icons
cd faenza_icons
unzip ../faenza_icons.zip
tar xzf Faenza.tar.gz
cd Faenza/mimetypes/96
mkdir ../icons_tmp
cp -L * ../icons_tmp/
cd ../icons_tmp
rm *.icon
rename "gnome-mime-" "" *
cd ..
mv icons_tmp ../../../
cd ../../..
cp -r icons_tmp/*-* ${THEME_STATIC_PATH}/faenzaicons/
cp icons_tmp/none.png ${THEME_STATIC_PATH}/faenzaicons/

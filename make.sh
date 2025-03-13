#!/bin/bash

PRISMJS_VERSION="76dde18a575831c91491895193f56081ac08b0c5" # v1.30.0
DELFT_VERSION="dd693fcf7cbd2d9fb689fada278a58f4b3e43a75" # v1.15

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

# iconpack-delft
mkdir ${THEME_STATIC_PATH}/delficons
git clone https://github.com/madmaxms/iconpack-delft.git
cd iconpack-delft
git checkout ${DELFT_VERSION}
cd Delft/mimetypes/96
ln -s audio-x-vorbis+ogg.svg audio-vorbis.svg
mkdir ../icons_tmp
cp -L * ../icons_tmp/
cd ..
mv icons_tmp ../../../
cd ../../..
cp -r icons_tmp/* ${THEME_STATIC_PATH}/delfticons/

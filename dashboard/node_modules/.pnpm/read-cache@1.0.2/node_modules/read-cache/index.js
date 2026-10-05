var fs = require('fs');
var path = require('path');

var resolve = path.resolve;

function stat(path) {
	return new Promise(function (resolve, reject) {
		fs.stat(path, function (err, stats) {
			if (err) {
				reject(err);
				return;
			}
			resolve(stats);
		});
	});
}

function readFile(path) {
	return new Promise(function (resolve, reject) {
		fs.readFile(path, function (err, data) {
			if (err) {
				reject(err);
				return;
			}
			resolve(data);
		});
	});
}

var cache = Object.create(null);

function convert(content, encoding) {
	if (Buffer.isEncoding(encoding)) {
		return content.toString(encoding);
	}
	return content;
}

module.exports = function (path, encoding) {
	path = resolve(path);

	return stat(path).then(function (stats) {
		var item = cache[path];

		if (item && item.mtime.getTime() === stats.mtime.getTime()) {
			return convert(item.content, encoding);
		}

		return readFile(path).then(function (data) {
			cache[path] = {
				mtime: stats.mtime,
				content: data
			};

			return convert(data, encoding);
		});
	}).catch(function (err) {
		cache[path] = null;
		return Promise.reject(err);
	});
};

module.exports.sync = function (path, encoding) {
	path = resolve(path);

	try {
		var stats = fs.statSync(path);
		var item = cache[path];

		if (item && item.mtime.getTime() === stats.mtime.getTime()) {
			return convert(item.content, encoding);
		}

		var data = fs.readFileSync(path);

		cache[path] = {
			mtime: stats.mtime,
			content: data
		};

		return convert(data, encoding);
	} catch (err) {
		cache[path] = null;
		throw err;
	}

};

module.exports.get = function (path, encoding) {
	path = resolve(path);
	if (cache[path]) {
		return convert(cache[path].content, encoding);
	}
	return null;
};

module.exports.clear = function () {
	cache = Object.create(null);
};
